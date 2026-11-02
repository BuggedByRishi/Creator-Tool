// Package brands is the creator's brand contact book.
package brands

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rishi/creatorhq/internal/audit"
	"github.com/rishi/creatorhq/internal/auth"
	"github.com/rishi/creatorhq/internal/httpx"
)

type Brand struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Website    string    `json:"website"`
	Domain     string    `json:"domain"`
	GSTIN      string    `json:"gstin"`
	Notes      string    `json:"notes"`
	DealCount  int       `json:"deal_count"`
	PaidPaise  int64     `json:"paid_paise"` // lifetime value of paid deals
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Contact struct {
	ID        uuid.UUID `json:"id"`
	BrandID   uuid.UUID `json:"brand_id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type Handler struct{ DB *pgxpool.Pool }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/brands", h.list)
	r.Post("/brands", h.create)
	r.Get("/brands/{id}", h.get)
	r.Patch("/brands/{id}", h.update)
	r.Delete("/brands/{id}", h.delete)
	r.Get("/brands/{id}/contacts", h.listContacts)
	r.Post("/brands/{id}/contacts", h.createContact)
	r.Delete("/brands/{id}/contacts/{contactID}", h.deleteContact)
}

const brandCols = `
	b.id, b.name, b.website, b.domain, b.gstin, b.notes,
	(SELECT count(*) FROM deals d WHERE d.brand_id = b.id),
	(SELECT COALESCE(sum(amount_paise), 0) FROM deals d WHERE d.brand_id = b.id AND d.stage = 'paid'),
	b.created_at, b.updated_at`

func scanBrand(row pgx.Row) (Brand, error) {
	var b Brand
	err := row.Scan(&b.ID, &b.Name, &b.Website, &b.Domain, &b.GSTIN, &b.Notes, &b.DealCount, &b.PaidPaise, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := h.DB.Query(r.Context(), `SELECT `+brandCols+` FROM brands b
		WHERE b.creator_id = $1 AND ($2 = '' OR b.name ILIKE '%' || $2 || '%' OR b.domain ILIKE '%' || $2 || '%')
		ORDER BY lower(b.name) LIMIT $3`, id.CreatorID, q, httpx.IntQuery(r, "limit", 100, 500))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Brand, error) { return scanBrand(row) })
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"brands": out})
}

type brandInput struct {
	Name    *string `json:"name"`
	Website *string `json:"website"`
	GSTIN   *string `json:"gstin"`
	Notes   *string `json:"notes"`
}

func (in *brandInput) normalise() (domain *string, err error) {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return nil, httpx.BadRequest("name is required")
		}
		in.Name = &n
	}
	if in.GSTIN != nil {
		g := strings.ToUpper(strings.TrimSpace(*in.GSTIN))
		if g != "" && !auth.ValidGSTIN(g) {
			return nil, httpx.BadRequest("invalid GSTIN")
		}
		in.GSTIN = &g
	}
	if in.Website != nil {
		d := domainOf(*in.Website)
		domain = &d
	}
	return domain, nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	var in brandInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if in.Name == nil {
		httpx.Fail(w, r, httpx.BadRequest("name is required"))
		return
	}
	domain, err := in.normalise()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var bid uuid.UUID
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO brands (creator_id, name, website, domain, gstin, notes)
		VALUES ($1, $2, COALESCE($3, ''), COALESCE($4, ''), COALESCE($5, ''), COALESCE($6, ''))
		RETURNING id`, id.CreatorID, in.Name, in.Website, domain, in.GSTIN, in.Notes).Scan(&bid)
	if isUnique(err) {
		httpx.Fail(w, r, httpx.Conflict("a brand with this name already exists"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "create", "brand", bid.String(), nil)
	h.respond(w, r, id.CreatorID, bid, http.StatusCreated)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h.respond(w, r, auth.FromContext(r.Context()).CreatorID, bid, http.StatusOK)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	bid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in brandInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	domain, err := in.normalise()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `
		UPDATE brands SET
			name = COALESCE($3, name), website = COALESCE($4, website), domain = COALESCE($5, domain),
			gstin = COALESCE($6, gstin), notes = COALESCE($7, notes), updated_at = now()
		WHERE id = $1 AND creator_id = $2`,
		bid, id.CreatorID, in.Name, in.Website, domain, in.GSTIN, in.Notes)
	if isUnique(err) {
		httpx.Fail(w, r, httpx.Conflict("a brand with this name already exists"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Fail(w, r, httpx.NotFound("brand"))
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "update", "brand", bid.String(), nil)
	h.respond(w, r, id.CreatorID, bid, http.StatusOK)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	bid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM brands WHERE id = $1 AND creator_id = $2`, bid, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Fail(w, r, httpx.NotFound("brand"))
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "delete", "brand", bid.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, creatorID, bid uuid.UUID, status int) {
	b, err := scanBrand(h.DB.QueryRow(r.Context(),
		`SELECT `+brandCols+` FROM brands b WHERE b.id = $1 AND b.creator_id = $2`, bid, creatorID))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NotFound("brand"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, status, b)
}

// ---- contacts ----

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	bid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT id, brand_id, name, email, phone, role, created_at FROM brand_contacts
		WHERE brand_id = $1 AND creator_id = $2 ORDER BY created_at`, bid, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Contact, error) {
		var c Contact
		err := row.Scan(&c.ID, &c.BrandID, &c.Name, &c.Email, &c.Phone, &c.Role, &c.CreatedAt)
		return c, err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"contacts": out})
}

type contactInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
	Role  string `json:"role"`
}

func (h *Handler) createContact(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	bid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in contactInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" && strings.TrimSpace(in.Email) == "" {
		httpx.Fail(w, r, httpx.BadRequest("name or email is required"))
		return
	}
	var c Contact
	// The INSERT ... SELECT only inserts if the brand belongs to this creator.
	err = h.DB.QueryRow(r.Context(), `
		INSERT INTO brand_contacts (brand_id, creator_id, name, email, phone, role)
		SELECT id, creator_id, $3, lower($4), $5, $6 FROM brands WHERE id = $1 AND creator_id = $2
		RETURNING id, brand_id, name, email, phone, role, created_at`,
		bid, id.CreatorID, strings.TrimSpace(in.Name), strings.TrimSpace(in.Email), in.Phone, in.Role).
		Scan(&c.ID, &c.BrandID, &c.Name, &c.Email, &c.Phone, &c.Role, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NotFound("brand"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, c)
}

func (h *Handler) deleteContact(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	cid, err := httpx.UUIDParam(r, "contactID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM brand_contacts WHERE id = $1 AND creator_id = $2`, cid, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Fail(w, r, httpx.NotFound("contact"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func domainOf(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
