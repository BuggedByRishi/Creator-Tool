// Package auth handles sign-up, login and cookie sessions, and resolves
// which creator workspace each request acts on.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/rishi/creatorhq/internal/audit"
	"github.com/rishi/creatorhq/internal/httpx"
)

const cookieName = "chq_session"

// Identity is attached to every authenticated request.
type Identity struct {
	UserID    uuid.UUID
	CreatorID uuid.UUID
	Role      string
}

// CanWrite reports whether the member may create or change workspace data.
func (i Identity) CanWrite() bool { return i.Role == "owner" || i.Role == "manager" || i.Role == "editor" }

// RequireWrite blocks viewers from non-GET requests.
func RequireWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !FromContext(r.Context()).CanWrite() {
			httpx.Fail(w, r, httpx.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey struct{}

func FromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(ctxKey{}).(Identity)
	return id
}

type Service struct {
	DB           *pgxpool.Pool
	TTL          time.Duration
	CookieSecure bool
}

func (s *Service) Routes(r chi.Router, authed func(http.Handler) http.Handler) {
	r.Post("/auth/signup", s.signup)
	r.Post("/auth/login", s.login)
	r.With(authed).Post("/auth/logout", s.logout)
	r.With(authed).Get("/me", s.me)
	r.With(authed).Patch("/creator", s.updateCreator)
}

// ---- handlers ----

type signupReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"` // creator / channel name
}

func (s *Service) signup(w http.ResponseWriter, r *http.Request) {
	var req signupReq
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if _, err := mail.ParseAddress(req.Email); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("invalid email"))
		return
	}
	if len(req.Password) < 10 || len(req.Password) > 72 {
		httpx.Fail(w, r, httpx.BadRequest("password must be 10 to 72 characters"))
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = req.Name
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	var userID, creatorID uuid.UUID
	err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(),
			`INSERT INTO users (email, password_hash, name) VALUES ($1, $2, $3) RETURNING id`,
			req.Email, string(hash), strings.TrimSpace(req.Name)).Scan(&userID); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(),
			`INSERT INTO creators (display_name) VALUES ($1) RETURNING id`,
			strings.TrimSpace(req.DisplayName)).Scan(&creatorID); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(),
			`INSERT INTO creator_members (creator_id, user_id, role) VALUES ($1, $2, 'owner')`, creatorID, userID)
		return err
	})
	if isUniqueViolation(err) {
		httpx.Fail(w, r, httpx.Conflict("an account with this email already exists"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, userID, creatorID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), s.DB, creatorID, userID, "signup", "user", userID.String(), nil)
	httpx.JSON(w, http.StatusCreated, map[string]any{"user_id": userID, "creator_id": creatorID})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// dummyHash keeps login timing similar whether or not the email exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var userID uuid.UUID
	var hash string
	err := s.DB.QueryRow(r.Context(),
		`SELECT id, password_hash FROM users WHERE lower(email) = lower($1)`,
		strings.TrimSpace(req.Email)).Scan(&userID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		httpx.Fail(w, r, &httpx.Error{Status: http.StatusUnauthorized, Code: "invalid_credentials", Message: "wrong email or password"})
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		httpx.Fail(w, r, &httpx.Error{Status: http.StatusUnauthorized, Code: "invalid_credentials", Message: "wrong email or password"})
		return
	}
	// Default workspace: the first creator this user belongs to.
	var creatorID uuid.UUID
	err = s.DB.QueryRow(r.Context(),
		`SELECT creator_id FROM creator_members WHERE user_id = $1 ORDER BY created_at LIMIT 1`, userID).Scan(&creatorID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, userID, creatorID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), s.DB, creatorID, userID, "login", "user", userID.String(), nil)
	httpx.JSON(w, http.StatusOK, map[string]any{"user_id": userID, "creator_id": creatorID})
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash = $1`, hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

type Creator struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	LegalName   string    `json:"legal_name"`
	Niche       string    `json:"niche"`
	City        string    `json:"city"`
	Language    string    `json:"language"`
	GSTIN       string    `json:"gstin"`
	StateCode   string    `json:"state_code"`
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r.Context())
	var email, name string
	if err := s.DB.QueryRow(r.Context(), `SELECT email, name FROM users WHERE id = $1`, id.UserID).Scan(&email, &name); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	c, err := s.getCreator(r.Context(), id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"user":    map[string]any{"id": id.UserID, "email": email, "name": name},
		"creator": c,
		"role":    id.Role,
	})
}

type updateCreatorReq struct {
	DisplayName *string `json:"display_name"`
	LegalName   *string `json:"legal_name"`
	Niche       *string `json:"niche"`
	City        *string `json:"city"`
	Language    *string `json:"language"`
	GSTIN       *string `json:"gstin"`
	StateCode   *string `json:"state_code"`
}

func (s *Service) updateCreator(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r.Context())
	if id.Role != "owner" && id.Role != "manager" {
		httpx.Fail(w, r, httpx.ErrForbidden)
		return
	}
	var req updateCreatorReq
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.GSTIN != nil {
		g := strings.ToUpper(strings.TrimSpace(*req.GSTIN))
		if g != "" && !ValidGSTIN(g) {
			httpx.Fail(w, r, httpx.BadRequest("invalid GSTIN"))
			return
		}
		req.GSTIN = &g
		if g != "" && req.StateCode == nil {
			sc := g[:2] // first two digits of a GSTIN are the state code
			req.StateCode = &sc
		}
	}
	_, err := s.DB.Exec(r.Context(), `
		UPDATE creators SET
			display_name = COALESCE($2, display_name),
			legal_name   = COALESCE($3, legal_name),
			niche        = COALESCE($4, niche),
			city         = COALESCE($5, city),
			language     = COALESCE($6, language),
			gstin        = COALESCE($7, gstin),
			state_code   = COALESCE($8, state_code),
			updated_at   = now()
		WHERE id = $1`,
		id.CreatorID, req.DisplayName, req.LegalName, req.Niche, req.City, req.Language, req.GSTIN, req.StateCode)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	c, err := s.getCreator(r.Context(), id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), s.DB, id.CreatorID, id.UserID, "update", "creator", id.CreatorID.String(), nil)
	httpx.JSON(w, http.StatusOK, c)
}

func (s *Service) getCreator(ctx context.Context, id uuid.UUID) (Creator, error) {
	var c Creator
	err := s.DB.QueryRow(ctx, `
		SELECT id, display_name, legal_name, niche, city, language, gstin, state_code
		FROM creators WHERE id = $1`, id).
		Scan(&c.ID, &c.DisplayName, &c.LegalName, &c.Niche, &c.City, &c.Language, &c.GSTIN, &c.StateCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NotFound("creator")
	}
	return c, err
}

// ---- sessions ----

func (s *Service) startSession(w http.ResponseWriter, r *http.Request, userID, creatorID uuid.UUID) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().Add(s.TTL)
	var active any
	if creatorID != uuid.Nil {
		active = creatorID
	}
	_, err := s.DB.Exec(r.Context(), `
		INSERT INTO sessions (token_hash, user_id, active_creator_id, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		hashToken(token), userID, active, truncate(r.UserAgent(), 256), clientIP(r), expires)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Middleware rejects requests without a valid session and loads the Identity.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" {
			httpx.Fail(w, r, httpx.ErrUnauthorized)
			return
		}
		var id Identity
		var creatorID *uuid.UUID
		var role *string
		err = s.DB.QueryRow(r.Context(), `
			SELECT s.user_id, s.active_creator_id, m.role
			FROM sessions s
			LEFT JOIN creator_members m ON m.creator_id = s.active_creator_id AND m.user_id = s.user_id
			WHERE s.token_hash = $1 AND s.expires_at > now()`, hashToken(c.Value)).
			Scan(&id.UserID, &creatorID, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Fail(w, r, httpx.ErrUnauthorized)
			return
		}
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if creatorID == nil || role == nil {
			httpx.Fail(w, r, &httpx.Error{Status: http.StatusForbidden, Code: "no_workspace", Message: "no creator workspace selected"})
			return
		}
		id.CreatorID, id.Role = *creatorID, *role
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// CleanupExpired deletes expired sessions; call it periodically.
func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}

// ---- helpers ----

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func clientIP(r *http.Request) string {
	// chi's RealIP middleware has already applied X-Forwarded-For / X-Real-IP.
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ValidGSTIN checks the 15-character GSTIN format and its check digit.
func ValidGSTIN(g string) bool {
	if len(g) != 15 {
		return false
	}
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	sum := 0
	for i := 0; i < 14; i++ {
		v := strings.IndexByte(chars, g[i])
		if v < 0 {
			return false
		}
		f := 1
		if i%2 == 1 {
			f = 2
		}
		p := v * f
		sum += p/36 + p%36
	}
	check := (36 - sum%36) % 36
	return g[14] == chars[check]
}
