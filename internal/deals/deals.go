// Package deals is the creator's deal pipeline (CRM) and deliverables.
package deals

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rishi/creatorhq/internal/audit"
	"github.com/rishi/creatorhq/internal/auth"
	"github.com/rishi/creatorhq/internal/httpx"
)

// Stages in pipeline order. "lost" sits outside the happy path.
var Stages = []string{"inquiry", "negotiating", "contracted", "in_production", "posted", "invoiced", "paid", "lost"}

var dealTypes = []string{"paid", "barter", "affiliate", "hybrid"}

type Deal struct {
	ID               uuid.UUID  `json:"id"`
	BrandID          *uuid.UUID `json:"brand_id"`
	BrandName        *string    `json:"brand_name"`
	Title            string     `json:"title"`
	Stage            string     `json:"stage"`
	LostReason       string     `json:"lost_reason"`
	AmountPaise      int64      `json:"amount_paise"`
	Currency         string     `json:"currency"`
	DealType         string     `json:"deal_type"`
	UsageRights      string     `json:"usage_rights"`
	ExclusivityDays  int        `json:"exclusivity_days"`
	PaymentTermsDays int        `json:"payment_terms_days"`
	Source           string     `json:"source"`
	Notes            string     `json:"notes"`
	DeliverablesDone int        `json:"deliverables_done"`
	DeliverablesAll  int        `json:"deliverables_total"`
	NextDueAt        *time.Time `json:"next_due_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ClosedAt         *time.Time `json:"closed_at"`
}

type Handler struct{ DB *pgxpool.Pool }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/deals", h.list)
	r.Get("/deals/pipeline", h.pipeline)
	r.Post("/deals", h.create)
	r.Get("/deals/{id}", h.get)
	r.Patch("/deals/{id}", h.update)
	r.Delete("/deals/{id}", h.delete)
	r.Post("/deals/{id}/stage", h.moveStage)
	r.Get("/deals/{id}/history", h.history)

	r.Get("/deals/{id}/deliverables", h.listDeliverables)
	r.Post("/deals/{id}/deliverables", h.createDeliverable)
	r.Patch("/deliverables/{id}", h.updateDeliverable)
	r.Delete("/deliverables/{id}", h.deleteDeliverable)
	r.Get("/deliverables/upcoming", h.upcomingDeliverables)
}

const dealCols = `
	d.id, d.brand_id, b.name, d.title, d.stage, d.lost_reason, d.amount_paise, d.currency, d.deal_type,
	d.usage_rights, d.exclusivity_days, d.payment_terms_days, d.source, d.notes,
	(SELECT count(*) FILTER (WHERE status = 'posted') FROM deliverables x WHERE x.deal_id = d.id),
	(SELECT count(*) FROM deliverables x WHERE x.deal_id = d.id),
	(SELECT min(due_at) FROM deliverables x WHERE x.deal_id = d.id AND x.status <> 'posted'),
	d.created_at, d.updated_at, d.closed_at`

const dealFrom = ` FROM deals d LEFT JOIN brands b ON b.id = d.brand_id `

func scanDeal(row pgx.Row) (Deal, error) {
	var d Deal
	err := row.Scan(&d.ID, &d.BrandID, &d.BrandName, &d.Title, &d.Stage, &d.LostReason, &d.AmountPaise,
		&d.Currency, &d.DealType, &d.UsageRights, &d.ExclusivityDays, &d.PaymentTermsDays, &d.Source, &d.Notes,
		&d.DeliverablesDone, &d.DeliverablesAll, &d.NextDueAt, &d.CreatedAt, &d.UpdatedAt, &d.ClosedAt)
	return d, err
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	q := r.URL.Query()
	stage := q.Get("stage")
	if stage != "" && !slices.Contains(Stages, stage) {
		httpx.Fail(w, r, httpx.BadRequest("unknown stage"))
		return
	}
	var brandID *uuid.UUID
	if v := q.Get("brand_id"); v != "" {
		b, err := uuid.Parse(v)
		if err != nil {
			httpx.Fail(w, r, httpx.BadRequest("invalid brand_id"))
			return
		}
		brandID = &b
	}
	rows, err := h.DB.Query(r.Context(), `SELECT `+dealCols+dealFrom+`
		WHERE d.creator_id = $1
		  AND ($2 = '' OR d.stage = $2)
		  AND ($3::uuid IS NULL OR d.brand_id = $3)
		  AND ($4 = '' OR d.title ILIKE '%' || $4 || '%' OR b.name ILIKE '%' || $4 || '%')
		ORDER BY d.updated_at DESC LIMIT $5`,
		id.CreatorID, stage, brandID, strings.TrimSpace(q.Get("q")), httpx.IntQuery(r, "limit", 100, 500))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Deal, error) { return scanDeal(row) })
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deals": out})
}

// pipeline returns every stage in order with its deals and totals — one call
// renders the whole kanban board.
func (h *Handler) pipeline(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	rows, err := h.DB.Query(r.Context(), `SELECT `+dealCols+dealFrom+`
		WHERE d.creator_id = $1
		  AND (d.stage NOT IN ('paid','lost') OR d.closed_at > now() - interval '30 days')
		ORDER BY d.updated_at DESC`, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Deal, error) { return scanDeal(row) })
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	type column struct {
		Stage      string `json:"stage"`
		Count      int    `json:"count"`
		TotalPaise int64  `json:"total_paise"`
		Deals      []Deal `json:"deals"`
	}
	cols := make([]column, len(Stages))
	idx := map[string]int{}
	for i, s := range Stages {
		cols[i] = column{Stage: s, Deals: []Deal{}}
		idx[s] = i
	}
	var openPaise int64
	for _, d := range all {
		c := &cols[idx[d.Stage]]
		c.Deals = append(c.Deals, d)
		c.Count++
		c.TotalPaise += d.AmountPaise
		if d.Stage != "paid" && d.Stage != "lost" {
			openPaise += d.AmountPaise
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"columns": cols, "open_pipeline_paise": openPaise})
}

type dealInput struct {
	BrandID          *uuid.UUID `json:"brand_id"`
	Title            *string    `json:"title"`
	AmountPaise      *int64     `json:"amount_paise"`
	Currency         *string    `json:"currency"`
	DealType         *string    `json:"deal_type"`
	UsageRights      *string    `json:"usage_rights"`
	ExclusivityDays  *int       `json:"exclusivity_days"`
	PaymentTermsDays *int       `json:"payment_terms_days"`
	Notes            *string    `json:"notes"`
}

func (in *dealInput) validate() error {
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return httpx.BadRequest("title is required")
		}
		in.Title = &t
	}
	if in.AmountPaise != nil && *in.AmountPaise < 0 {
		return httpx.BadRequest("amount_paise must be >= 0")
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if len(c) != 3 {
			return httpx.BadRequest("currency must be a 3-letter ISO code")
		}
		in.Currency = &c
	}
	if in.DealType != nil && !slices.Contains(dealTypes, *in.DealType) {
		return httpx.BadRequest("deal_type must be one of paid, barter, affiliate, hybrid")
	}
	if in.ExclusivityDays != nil && *in.ExclusivityDays < 0 {
		return httpx.BadRequest("exclusivity_days must be >= 0")
	}
	if in.PaymentTermsDays != nil && *in.PaymentTermsDays < 0 {
		return httpx.BadRequest("payment_terms_days must be >= 0")
	}
	return nil
}

func (h *Handler) checkBrand(ctx context.Context, creatorID uuid.UUID, brandID *uuid.UUID) error {
	if brandID == nil {
		return nil
	}
	var ok bool
	if err := h.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE id = $1 AND creator_id = $2)`,
		*brandID, creatorID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return httpx.BadRequest("brand_id does not exist")
	}
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	var in dealInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if in.Title == nil {
		httpx.Fail(w, r, httpx.BadRequest("title is required"))
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := h.checkBrand(r.Context(), id.CreatorID, in.BrandID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var did uuid.UUID
	err := h.DB.QueryRow(r.Context(), `
		INSERT INTO deals (creator_id, brand_id, title, amount_paise, currency, deal_type,
		                   usage_rights, exclusivity_days, payment_terms_days, notes)
		VALUES ($1, $2, $3, COALESCE($4, 0), COALESCE($5, 'INR'), COALESCE($6, 'paid'),
		        COALESCE($7, ''), COALESCE($8, 0), COALESCE($9, 30), COALESCE($10, ''))
		RETURNING id`,
		id.CreatorID, in.BrandID, in.Title, in.AmountPaise, in.Currency, in.DealType,
		in.UsageRights, in.ExclusivityDays, in.PaymentTermsDays, in.Notes).Scan(&did)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "create", "deal", did.String(), nil)
	h.respond(w, r, id.CreatorID, did, http.StatusCreated)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h.respond(w, r, auth.FromContext(r.Context()).CreatorID, did, http.StatusOK)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in dealInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := h.checkBrand(r.Context(), id.CreatorID, in.BrandID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `
		UPDATE deals SET
			brand_id = COALESCE($3, brand_id), title = COALESCE($4, title),
			amount_paise = COALESCE($5, amount_paise), currency = COALESCE($6, currency),
			deal_type = COALESCE($7, deal_type), usage_rights = COALESCE($8, usage_rights),
			exclusivity_days = COALESCE($9, exclusivity_days),
			payment_terms_days = COALESCE($10, payment_terms_days),
			notes = COALESCE($11, notes), updated_at = now()
		WHERE id = $1 AND creator_id = $2`,
		did, id.CreatorID, in.BrandID, in.Title, in.AmountPaise, in.Currency, in.DealType,
		in.UsageRights, in.ExclusivityDays, in.PaymentTermsDays, in.Notes)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Fail(w, r, httpx.NotFound("deal"))
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "update", "deal", did.String(), nil)
	h.respond(w, r, id.CreatorID, did, http.StatusOK)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM deals WHERE id = $1 AND creator_id = $2`, did, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Fail(w, r, httpx.NotFound("deal"))
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "delete", "deal", did.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

type stageReq struct {
	Stage      string `json:"stage"`
	LostReason string `json:"lost_reason"`
}

// moveStage is what the kanban board calls on drag-and-drop. Any move is
// allowed (creators fix mistakes), and every move is recorded.
func (h *Handler) moveStage(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req stageReq
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !slices.Contains(Stages, req.Stage) {
		httpx.Fail(w, r, httpx.BadRequest("unknown stage"))
		return
	}
	err = pgx.BeginFunc(r.Context(), h.DB, func(tx pgx.Tx) error {
		var from string
		if err := tx.QueryRow(r.Context(),
			`SELECT stage FROM deals WHERE id = $1 AND creator_id = $2 FOR UPDATE`, did, id.CreatorID).Scan(&from); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("deal")
			}
			return err
		}
		if from == req.Stage {
			return nil
		}
		closing := req.Stage == "paid" || req.Stage == "lost"
		if _, err := tx.Exec(r.Context(), `
			UPDATE deals SET stage = $2,
				lost_reason = CASE WHEN $2 = 'lost' THEN $3 ELSE '' END,
				closed_at = CASE WHEN $4 THEN now() ELSE NULL END,
				updated_at = now()
			WHERE id = $1`, did, req.Stage, strings.TrimSpace(req.LostReason), closing); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `
			INSERT INTO deal_stage_events (deal_id, from_stage, to_stage, actor_user_id) VALUES ($1, $2, $3, $4)`,
			did, from, req.Stage, id.UserID)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	audit.Log(r.Context(), h.DB, id.CreatorID, id.UserID, "move_stage", "deal", did.String(), map[string]any{"to": req.Stage})
	h.respond(w, r, id.CreatorID, did, http.StatusOK)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	id := auth.FromContext(r.Context())
	did, err := httpx.UUIDParam(r, "id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rows, err := h.DB.Query(r.Context(), `
		SELECT e.from_stage, e.to_stage, e.created_at FROM deal_stage_events e
		JOIN deals d ON d.id = e.deal_id
		WHERE e.deal_id = $1 AND d.creator_id = $2 ORDER BY e.created_at`, did, id.CreatorID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	type event struct {
		From string    `json:"from"`
		To   string    `json:"to"`
		At   time.Time `json:"at"`
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (event, error) {
		var e event
		return e, row.Scan(&e.From, &e.To, &e.At)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"events": out})
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, creatorID, did uuid.UUID, status int) {
	d, err := scanDeal(h.DB.QueryRow(r.Context(),
		`SELECT `+dealCols+dealFrom+` WHERE d.id = $1 AND d.creator_id = $2`, did, creatorID))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NotFound("deal"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, status, d)
}
