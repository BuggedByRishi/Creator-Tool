package deals

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rishi/creatorhq/internal/audit"
	"github.com/rishi/creatorhq/internal/auth"
	"github.com/rishi/creatorhq/internal/httpx"
)

var (
	platforms           = []string{"instagram", "youtube", "x", "linkedin", "other"}
	deliverableStatuses = []string{"todo", "scripting", "shooting", "editing", "in_review", "approved", "posted"}
)

type Deliverable struct {
	ID        uuid.UUID  `json:"id"`
	DealID    uuid.UUID  `json:"deal_id"`
	Platform  string     `json:"platform"`
	Format    string     `json:"format"`
	Title     string     `json:"title"`
	DueAt     *time.Time `json:"due_at"`
	Status    string     `json:"status"`
	PostURL   string     `json:"post_url"`
	PostedAt  *time.Time `json:"posted_at"`
	Notes     string     `json:"notes"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

const delCols = `id, deal_id, platform, format, title, due_at, status, post_url, posted_at, notes, created_at, updated_at`

func scanDeliverable(row pgx.Row) (Deliverable, error) {
	var d Deliverable
	err := row.Scan(&d.ID, &d.DealID, &d.Platform, &d.Format, &d.Title, &d.DueAt, &d.Status,
		&d.PostURL, &d.PostedAt, &d.Notes, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

type deliverableInput struct {
	Platform *string    `json:"platform"`
	Format   *string    `json:"format"`
	Title    *string    `json:"title"`
	DueAt    *time.Time `json:"due_at"`
	Status   *string    `json:"status"`
	PostURL  *string    `json:"post_url"`
	Notes    *string    `json:"notes"`
}

func (in *deliverableInput) validate() error {
	if in.Platform != nil && !slices.Contains(platforms, *in.Platform) {
		return httpx.BadRequest("platform must be one of instagram, youtube, x, linkedin, other")
	}
	if in.Status != nil && !slices.Contains(deliverableStatuses, *in.Status) {
		return httpx.BadRequest("unknown status")
	}
	if in.PostURL != nil {
		u := strings.TrimSpace(*in.PostURL)
		if u != "" && !strings.HasPrefix(u, "https://") {
			return httpx.BadRequest("post_url must start with https://")
		}
		in.PostURL = &u
	}
	return nil
}

func (h *Handler) listDeliverables(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT `+delCols+` FROM deliverables
		WHERE deal_id = $1 AND creator_id = $2 ORDER BY due_at NULLS LAST, created_at`, did, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Deliverable, error) { return scanDeliverable(row) })
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deliverables": out})
}

// upcomingDeliverables powers the "due this week" widget and, later, reminders.
func (h *Handler) upcomingDeliverables(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	days := httpx.IntQuery(r, "days", 7, 90)
	rows, err := h.DB.Query(r.Context(), `SELECT `+delCols+` FROM deliverables
		WHERE creator_id = $1 AND status <> 'posted' AND due_at IS NOT NULL
		  AND due_at < now() + make_interval(days => $2)
		ORDER BY due_at`, id.CreatorID, days)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Deliverable, error) { return scanDeliverable(row) })
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deliverables": out})
}

func (h *Handler) createDeliverable(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in deliverableInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if in.Platform == nil {
		httpx.Fail(w, r, httpx.BadRequest("platform is required"))
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := scanDeliverable(h.DB.QueryRow(r.Context(), `
		INSERT INTO deliverables (deal_id, creator_id, platform, format, title, due_at, status, post_url, notes, posted_at)
		SELECT id, creator_id, $3, COALESCE($4, ''), COALESCE($5, ''), $6, COALESCE($7, 'todo'),
		       COALESCE($8, ''), COALESCE($9, ''), CASE WHEN $7 = 'posted' THEN now() END
		FROM deals WHERE id = $1 AND creator_id = $2
		RETURNING `+delCols,
		did, id.CreatorID, in.Platform, in.Format, in.Title, in.DueAt, in.Status, in.PostURL, in.Notes))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NotFound("deal"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "create", "deliverable", d.ID.String(), nil)
	httpx.JSON(w, http.StatusCreated, d)
}

func (h *Handler) updateDeliverable(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	xid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in deliverableInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := scanDeliverable(h.DB.QueryRow(r.Context(), `
		UPDATE deliverables SET
			platform = COALESCE($3, platform), format = COALESCE($4, format), title = COALESCE($5, title),
			due_at = COALESCE($6, due_at), status = COALESCE($7, status), post_url = COALESCE($8, post_url),
			notes = COALESCE($9, notes),
			posted_at = CASE
				WHEN $7 = 'posted' AND status <> 'posted' THEN now()
				WHEN $7 IS NOT NULL AND $7 <> 'posted' THEN NULL
				ELSE posted_at END,
			updated_at = now()
		WHERE id = $1 AND creator_id = $2
		RETURNING `+delCols,
		xid, id.CreatorID, in.Platform, in.Format, in.Title, in.DueAt, in.Status, in.PostURL, in.Notes))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NotFound("deliverable"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "update", "deliverable", xid.String(), nil)
	httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) deleteDeliverable(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	xid, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM deliverables WHERE id = $1 AND creator_id = $2`, xid, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Fail(w, r, httpx.NotFound("deliverable"))
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "delete", "deliverable", xid.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}
