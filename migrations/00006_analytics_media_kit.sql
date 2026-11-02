-- +goose Up
-- Daily platform stats (from official YouTube / Instagram APIs) and the
-- public media kit page.

CREATE TABLE metrics_snapshots (
    id                   BIGSERIAL PRIMARY KEY,
    creator_id           UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    connected_account_id UUID REFERENCES connected_accounts(id) ON DELETE CASCADE, -- NULL for manual entry
    platform             TEXT NOT NULL CHECK (platform IN ('youtube','instagram')),
    captured_on          DATE NOT NULL,
    source               TEXT NOT NULL DEFAULT 'api' CHECK (source IN ('api','manual')),
    followers            BIGINT NOT NULL DEFAULT 0,
    posts_count          INT    NOT NULL DEFAULT 0,
    views_28d            BIGINT NOT NULL DEFAULT 0,
    reach_28d            BIGINT NOT NULL DEFAULT 0,
    avg_views_per_post   BIGINT NOT NULL DEFAULT 0,
    engagement_rate_bp   INT    NOT NULL DEFAULT 0,  -- basis points: 345 = 3.45%
    -- {"age": {"18-24": 0.41, ...}, "gender": {"m": 0.62, ...}, "cities": {"Pune": 0.12, ...}}
    audience             JSONB NOT NULL DEFAULT '{}'::jsonb,
    raw                  JSONB NOT NULL DEFAULT '{}'::jsonb,   -- full API response, for re-processing
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (creator_id, platform, captured_on, source)
);
CREATE INDEX metrics_snapshots_latest_idx ON metrics_snapshots (creator_id, platform, captured_on DESC);

CREATE TABLE media_kits (
    creator_id     UUID PRIMARY KEY REFERENCES creators(id) ON DELETE CASCADE,
    slug           TEXT NOT NULL CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{1,38}[a-z0-9])$'),
    is_public      BOOLEAN NOT NULL DEFAULT false,
    headline       TEXT NOT NULL DEFAULT '',
    bio            TEXT NOT NULL DEFAULT '',
    contact_email  TEXT NOT NULL DEFAULT '',
    -- [{"platform":"instagram","format":"reel","price_paise":3000000}, ...]
    rate_card      JSONB NOT NULL DEFAULT '[]'::jsonb,
    show_rates     BOOLEAN NOT NULL DEFAULT false,
    featured_brand_ids UUID[] NOT NULL DEFAULT '{}',   -- past brands to show as logos
    theme          JSONB NOT NULL DEFAULT '{}'::jsonb,
    view_count     BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX media_kits_slug_uniq ON media_kits (lower(slug));

-- +goose Down
DROP TABLE IF EXISTS media_kits;
DROP TABLE IF EXISTS metrics_snapshots;
