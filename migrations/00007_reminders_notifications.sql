-- +goose Up
-- Scheduled reminders (brand follow-ups, deliverables due, overdue invoices)
-- and the in-app notification feed.

CREATE TABLE reminders (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id  UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN
                  ('brand_follow_up','deliverable_due','invoice_overdue','watch_renewal','custom')),
    entity      TEXT NOT NULL,                 -- 'deal', 'deliverable', 'invoice', 'email_thread' ...
    entity_id   UUID NOT NULL,
    step        INT  NOT NULL DEFAULT 0,       -- e.g. overdue reminder #1 (7d), #2 (15d), #3 (30d)
    due_at      TIMESTAMPTZ NOT NULL,
    channel     TEXT NOT NULL DEFAULT 'in_app' CHECK (channel IN ('in_app','email','whatsapp')),
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','cancelled','failed')),
    attempts    INT  NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    sent_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- the same reminder step is never scheduled twice for one entity
    UNIQUE (kind, entity, entity_id, step)
);
-- The worker polls this: SELECT ... WHERE status='pending' AND due_at <= now() FOR UPDATE SKIP LOCKED
CREATE INDEX reminders_due_idx ON reminders (due_at) WHERE status = 'pending';
CREATE INDEX reminders_entity_idx ON reminders (entity, entity_id);

CREATE TABLE notifications (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    user_id    UUID REFERENCES users(id) ON DELETE CASCADE,  -- NULL = everyone in the workspace
    kind       TEXT NOT NULL,                  -- 'new_brand_offer', 'draft_ready', 'invoice_paid' ...
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    link       TEXT NOT NULL DEFAULT '',       -- app path to open, e.g. /inbox/<thread id>
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notifications_unread_idx ON notifications (creator_id, created_at DESC) WHERE read_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS reminders;
