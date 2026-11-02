// Package audit writes the append-only audit log. Failures are logged, never
// returned, so auditing can't break a user's request.
package audit

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Log(ctx context.Context, db *pgxpool.Pool, creatorID, userID uuid.UUID, action, entity, entityID string, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	_, err := db.Exec(ctx, `
		INSERT INTO audit_log (creator_id, user_id, action, entity, entity_id, meta)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		nullable(creatorID), nullable(userID), action, entity, entityID, meta)
	if err != nil {
		slog.Warn("audit log write failed", "err", err, "action", action, "entity", entity)
	}
}

func nullable(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}
