-- +goose Up
-- Foundation schema: identity, tenancy, brands, deals, deliverables, audit.
-- Money is stored as integer paise (BIGINT) to avoid float rounding.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uniq ON users (lower(email));

-- A creator is the business entity. One user can manage many creators
-- (agency mode) and one creator can have many team members.
CREATE TABLE creators (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name  TEXT NOT NULL,
    legal_name    TEXT NOT NULL DEFAULT '',
    niche         TEXT NOT NULL DEFAULT '',
    city          TEXT NOT NULL DEFAULT '',
    language      TEXT NOT NULL DEFAULT '',
    gstin         TEXT NOT NULL DEFAULT '',
    state_code    TEXT NOT NULL DEFAULT '',   -- GST place-of-supply state code, e.g. '27' for Maharashtra
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE creator_members (
    creator_id UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'manager', 'editor', 'viewer')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (creator_id, user_id)
);
CREATE INDEX creator_members_user_idx ON creator_members (user_id);

-- Only a SHA-256 hash of the session token is stored; the raw token lives in the cookie.
CREATE TABLE sessions (
    token_hash        BYTEA PRIMARY KEY,
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    active_creator_id UUID REFERENCES creators(id) ON DELETE SET NULL,
    user_agent        TEXT NOT NULL DEFAULT '',
    ip                TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE brands (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    website    TEXT NOT NULL DEFAULT '',
    domain     TEXT NOT NULL DEFAULT '',     -- used later to match inbound emails to brands
    gstin      TEXT NOT NULL DEFAULT '',     -- needed on B2B invoices
    notes      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX brands_creator_name_uniq ON brands (creator_id, lower(name));
CREATE INDEX brands_domain_idx ON brands (creator_id, domain);

CREATE TABLE brand_contacts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   UUID NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    creator_id UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL DEFAULT '',
    phone      TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX brand_contacts_brand_idx ON brand_contacts (brand_id);
CREATE INDEX brand_contacts_email_idx ON brand_contacts (creator_id, lower(email));

CREATE TABLE deals (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id        UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    brand_id          UUID REFERENCES brands(id) ON DELETE SET NULL,
    title             TEXT NOT NULL,
    stage             TEXT NOT NULL DEFAULT 'inquiry' CHECK (stage IN
                        ('inquiry','negotiating','contracted','in_production','posted','invoiced','paid','lost')),
    lost_reason       TEXT NOT NULL DEFAULT '',
    amount_paise      BIGINT NOT NULL DEFAULT 0 CHECK (amount_paise >= 0),
    currency          CHAR(3) NOT NULL DEFAULT 'INR',
    deal_type         TEXT NOT NULL DEFAULT 'paid' CHECK (deal_type IN ('paid','barter','affiliate','hybrid')),
    usage_rights      TEXT NOT NULL DEFAULT '',
    exclusivity_days  INT NOT NULL DEFAULT 0 CHECK (exclusivity_days >= 0),
    payment_terms_days INT NOT NULL DEFAULT 30 CHECK (payment_terms_days >= 0),
    source            TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','email','forward','marketplace')),
    notes             TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at         TIMESTAMPTZ
);
CREATE INDEX deals_creator_stage_idx ON deals (creator_id, stage, updated_at DESC);
CREATE INDEX deals_brand_idx ON deals (brand_id);

CREATE TABLE deal_stage_events (
    id            BIGSERIAL PRIMARY KEY,
    deal_id       UUID NOT NULL REFERENCES deals(id) ON DELETE CASCADE,
    from_stage    TEXT NOT NULL,
    to_stage      TEXT NOT NULL,
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX deal_stage_events_deal_idx ON deal_stage_events (deal_id, created_at);

CREATE TABLE deliverables (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deal_id     UUID NOT NULL REFERENCES deals(id) ON DELETE CASCADE,
    creator_id  UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL CHECK (platform IN ('instagram','youtube','x','linkedin','other')),
    format      TEXT NOT NULL DEFAULT '',     -- reel, story, short, integration, dedicated video ...
    title       TEXT NOT NULL DEFAULT '',
    due_at      TIMESTAMPTZ,
    status      TEXT NOT NULL DEFAULT 'todo' CHECK (status IN
                  ('todo','scripting','shooting','editing','in_review','approved','posted')),
    post_url    TEXT NOT NULL DEFAULT '',
    posted_at   TIMESTAMPTZ,
    notes       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX deliverables_deal_idx ON deliverables (deal_id);
CREATE INDEX deliverables_due_idx ON deliverables (creator_id, due_at) WHERE status <> 'posted';

-- Append-only record of who did what. AI actions get their own table later.
CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    creator_id  UUID REFERENCES creators(id) ON DELETE SET NULL,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT NOT NULL,
    entity      TEXT NOT NULL,
    entity_id   TEXT NOT NULL DEFAULT '',
    meta        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_creator_idx ON audit_log (creator_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS deliverables;
DROP TABLE IF EXISTS deal_stage_events;
DROP TABLE IF EXISTS deals;
DROP TABLE IF EXISTS brand_contacts;
DROP TABLE IF EXISTS brands;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS creator_members;
DROP TABLE IF EXISTS creators;
DROP TABLE IF EXISTS users;
