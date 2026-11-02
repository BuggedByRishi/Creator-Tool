-- +goose Up
-- Income and expenses for the profit dashboard.
-- MVP fills income_entries from paid invoices plus manual entries; platform
-- payout imports (AdSense, affiliates, courses) and expenses come later but
-- use the same tables.

CREATE TABLE income_entries (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id   UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    source       TEXT NOT NULL CHECK (source IN
                   ('brand_deal','adsense','affiliate','course','membership','tips','other')),
    description  TEXT NOT NULL DEFAULT '',
    amount_paise BIGINT NOT NULL CHECK (amount_paise >= 0),   -- amount received, in INR paise
    tds_paise    BIGINT NOT NULL DEFAULT 0 CHECK (tds_paise >= 0),
    original_currency     CHAR(3) NOT NULL DEFAULT 'INR',
    original_amount_minor BIGINT,                            -- e.g. USD cents for foreign payouts
    received_on  DATE NOT NULL,
    deal_id      UUID REFERENCES deals(id) ON DELETE SET NULL,
    payment_id   UUID UNIQUE REFERENCES payments(id) ON DELETE CASCADE,  -- one income row per payment
    import_ref   TEXT,                                       -- id from a payout import, for de-duplication
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX income_entries_creator_idx ON income_entries (creator_id, received_on DESC);
CREATE UNIQUE INDEX income_entries_import_uniq ON income_entries (creator_id, source, import_ref) WHERE import_ref IS NOT NULL;

CREATE TABLE expenses (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id          UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    category            TEXT NOT NULL CHECK (category IN
                          ('equipment','editing','software','travel','props','marketing','team','fees','other')),
    vendor              TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    amount_paise        BIGINT NOT NULL CHECK (amount_paise >= 0),  -- total paid, including GST
    gst_paise           BIGINT NOT NULL DEFAULT 0 CHECK (gst_paise >= 0), -- input tax credit candidate
    vendor_gstin        TEXT NOT NULL DEFAULT '',
    spent_on            DATE NOT NULL,
    deal_id             UUID REFERENCES deals(id) ON DELETE SET NULL,     -- cost of a specific deal
    receipt_storage_key TEXT NOT NULL DEFAULT '',
    ocr_data            JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (gst_paise <= amount_paise)
);
CREATE INDEX expenses_creator_idx ON expenses (creator_id, spent_on DESC);

-- +goose Down
DROP TABLE IF EXISTS expenses;
DROP TABLE IF EXISTS income_entries;
