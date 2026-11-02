// Package httpx holds small HTTP helpers shared by every module.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// Error is an API error with a stable machine-readable code.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func BadRequest(msg string) *Error { return &Error{http.StatusBadRequest, "bad_request", msg} }
func NotFound(what string) *Error  { return &Error{http.StatusNotFound, "not_found", what + " not found"} }
func Conflict(msg string) *Error   { return &Error{http.StatusConflict, "conflict", msg} }

var (
	ErrUnauthorized = &Error{http.StatusUnauthorized, "unauthorized", "login required"}
	ErrForbidden    = &Error{http.StatusForbidden, "forbidden", "not allowed"}
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Fail writes err as JSON. Unknown errors are logged and hidden from the client.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if errors.As(err, &e) {
		JSON(w, e.Status, map[string]any{"error": e})
		return
	}
	slog.Error("request failed", "err", err, "path", r.URL.Path, "request_id", middleware.GetReqID(r.Context()))
	JSON(w, http.StatusInternalServerError, map[string]any{
		"error": Error{Code: "internal", Message: "something went wrong"},
	})
}

// Decode reads a JSON body, rejecting unknown fields and bodies over 1 MB.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("request body is empty")
		}
		return BadRequest("invalid JSON: " + err.Error())
	}
	return nil
}

func UUIDParam(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, BadRequest("invalid " + name)
	}
	return id, nil
}

func IntQuery(r *http.Request, name string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// RequireJSON blocks state-changing requests that are not JSON. Browsers
// cannot send cross-site application/json without a CORS preflight, so
// together with SameSite cookies this is our CSRF defence.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.ContentLength != 0 {
				mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if mt != "application/json" {
					JSON(w, http.StatusUnsupportedMediaType, map[string]any{
						"error": Error{Code: "unsupported_media_type", Message: "use application/json"},
					})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("http",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"dur_ms", time.Since(start).Milliseconds(), "request_id", middleware.GetReqID(r.Context()))
	})
}

func CORS(origin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Origin") == origin {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Add("Vary", "Origin")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
