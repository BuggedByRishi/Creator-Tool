-- +goose Up
-- AI inbox: connected accounts, the forwarding address, matched email threads,
-- messages, and AI-drafted replies waiting for the creator's approval.

-- One row per connected provider account (Gmail, Outlook, YouTube, Instagram).
-- OAuth tokens are NOT stored here: token_ref points to the secret in
-- Vault / cloud KMS, as the spec requires.
CREATE TABLE connected_accounts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id          UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    provider            TEXT NOT NULL CHECK (provider IN ('gmail','outlook','youtube','instagram')),
    external_account_id TEXT NOT NULL,           -- Google sub, channel id, IG user id ...
    display_handle      TEXT NOT NULL DEFAULT '', -- email address, @handle or channel name
    scopes              TEXT[] NOT NULL DEFAULT '{}',
    token_ref           TEXT NOT NULL DEFAULT '', -- secret path / KMS key id, never the token itself
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','reauth_required','revoked','error')),
    sync_cursor         TEXT NOT NULL DEFAULT '', -- Gmail historyId, Graph deltaLink, etc.
    watch_expires_at    TIMESTAMPTZ,              -- Gmail push watch must be renewed before this
    last_synced_at      TIMESTAMPTZ,
    last_error          TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (creator_id, provider, external_account_id)
);
CREATE INDEX connected_accounts_watch_idx ON connected_accounts (watch_expires_at) WHERE status = 'active';

-- Forwarding inbox: brand emails forwarded to deals+<token>@<our domain>.
-- Works from day one, before Google's restricted-scope review.
CREATE TABLE inbound_aliases (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    token      TEXT NOT NULL UNIQUE,              -- random, unguessable
    is_active  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inbound_aliases_creator_idx ON inbound_aliases (creator_id);

CREATE TABLE email_threads (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id           UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    connected_account_id UUID REFERENCES connected_accounts(id) ON DELETE SET NULL, -- NULL for forwarded mail
    source               TEXT NOT NULL CHECK (source IN ('gmail','outlook','forward')),
    provider_thread_id   TEXT NOT NULL,           -- Gmail threadId, Graph conversationId, or our own id for forwards
    subject              TEXT NOT NULL DEFAULT '',
    brand_id             UUID REFERENCES brands(id) ON DELETE SET NULL,
    deal_id              UUID REFERENCES deals(id) ON DELETE SET NULL,
    classification       TEXT NOT NULL DEFAULT 'unclassified' CHECK (classification IN
                           ('unclassified','brand_offer','agency_pitch','pr_gifting','spam_scam','payment','other')),
    classification_confidence NUMERIC(4,3) CHECK (classification_confidence BETWEEN 0 AND 1),
    scam_risk            TEXT NOT NULL DEFAULT 'unknown' CHECK (scam_risk IN ('unknown','low','medium','high')),
    status               TEXT NOT NULL DEFAULT 'new' CHECK (status IN
                           ('new','needs_review','awaiting_brand','replied','archived')),
    last_message_at      TIMESTAMPTZ,
    last_inbound_at      TIMESTAMPTZ,             -- used for "reply within 24h" metric
    first_reply_at       TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (creator_id, source, provider_thread_id)
);
CREATE INDEX email_threads_inbox_idx ON email_threads (creator_id, status, last_message_at DESC);
CREATE INDEX email_threads_deal_idx  ON email_threads (deal_id);

-- Only messages in matched brand-deal threads are stored, never whole mailboxes.
CREATE TABLE email_messages (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id           UUID NOT NULL REFERENCES email_threads(id) ON DELETE CASCADE,
    creator_id          UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    provider_message_id TEXT NOT NULL,            -- Gmail id or RFC 5322 Message-ID
    direction           TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    from_email          TEXT NOT NULL,
    from_name           TEXT NOT NULL DEFAULT '',
    to_emails           TEXT[] NOT NULL DEFAULT '{}',
    cc_emails           TEXT[] NOT NULL DEFAULT '{}',
    subject             TEXT NOT NULL DEFAULT '',
    snippet             TEXT NOT NULL DEFAULT '',
    body_text           TEXT NOT NULL DEFAULT '',
    has_attachments     BOOLEAN NOT NULL DEFAULT false,
    sent_at             TIMESTAMPTZ NOT NULL,
    -- AI-extracted deal fields: brand, budget, deliverables, deadline, usage rights, exclusivity
    extracted           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (creator_id, provider_message_id)      -- makes re-syncs idempotent
);
CREATE INDEX email_messages_thread_idx ON email_messages (thread_id, sent_at);
CREATE INDEX email_messages_from_idx   ON email_messages (creator_id, lower(from_email));

-- AI reply drafts. Nothing is sent without the creator's approval.
CREATE TABLE email_drafts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id        UUID NOT NULL REFERENCES email_threads(id) ON DELETE CASCADE,
    creator_id       UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    intent           TEXT NOT NULL CHECK (intent IN ('accept','negotiate','decline','ask_brief','follow_up','custom')),
    body_text        TEXT NOT NULL,
    original_body    TEXT NOT NULL DEFAULT '',    -- AI version, to measure how much the creator edited
    status           TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','sending','sent','discarded','failed')),
    ai_action_id     UUID REFERENCES ai_actions(id) ON DELETE SET NULL,
    approved_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    approved_at      TIMESTAMPTZ,
    sent_message_id  UUID REFERENCES email_messages(id) ON DELETE SET NULL,
    error            TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- an approved/sent draft must record who approved it
    CHECK (status NOT IN ('approved','sending','sent') OR approved_by IS NOT NULL)
);
CREATE INDEX email_drafts_thread_idx ON email_drafts (thread_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS email_drafts;
DROP TABLE IF EXISTS email_messages;
DROP TABLE IF EXISTS email_threads;
DROP TABLE IF EXISTS inbound_aliases;
DROP TABLE IF EXISTS connected_accounts;
