-- +goose Up
-- GST invoices, payment links, payments, and an idempotency log for webhooks.

-- Addresses are required on a GST tax invoice.
ALTER TABLE creators
    ADD COLUMN address_line1 TEXT NOT NULL DEFAULT '',
    ADD COLUMN address_line2 TEXT NOT NULL DEFAULT '',
    ADD COLUMN pincode       TEXT NOT NULL DEFAULT '',
    ADD COLUMN pan           TEXT NOT NULL DEFAULT '',
    ADD COLUMN default_sac   TEXT NOT NULL DEFAULT '',    -- SAC code the creator usually bills under
    ADD COLUMN lut_number    TEXT NOT NULL DEFAULT '';    -- Letter of Undertaking, for zero-rated export invoices

ALTER TABLE brands
    ADD COLUMN legal_name    TEXT NOT NULL DEFAULT '',
    ADD COLUMN address_line1 TEXT NOT NULL DEFAULT '',
    ADD COLUMN address_line2 TEXT NOT NULL DEFAULT '',
    ADD COLUMN city          TEXT NOT NULL DEFAULT '',
    ADD COLUMN pincode       TEXT NOT NULL DEFAULT '',
    ADD COLUMN state_code    TEXT NOT NULL DEFAULT '',    -- decides CGST+SGST vs IGST
    ADD COLUMN country       CHAR(2) NOT NULL DEFAULT 'IN';

-- GST requires invoice numbers that are unique and consecutive within a
-- financial year (max 16 chars). The app takes the next number with
-- UPDATE ... SET next_number = next_number + 1 RETURNING, inside the
-- invoice-issuing transaction, so numbers never repeat or skip.
CREATE TABLE invoice_series (
    creator_id     UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    financial_year TEXT NOT NULL CHECK (financial_year ~ '^[0-9]{4}-[0-9]{2}$'),   -- e.g. '2026-27'
    prefix         TEXT NOT NULL DEFAULT 'INV',
    next_number    INT  NOT NULL DEFAULT 1 CHECK (next_number > 0),
    PRIMARY KEY (creator_id, financial_year)
);

CREATE TABLE invoices (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id          UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    deal_id             UUID REFERENCES deals(id) ON DELETE SET NULL,
    brand_id            UUID REFERENCES brands(id) ON DELETE SET NULL,
    invoice_number      TEXT,                     -- NULL while draft; assigned on issue
    financial_year      TEXT,
    status              TEXT NOT NULL DEFAULT 'draft' CHECK (status IN
                          ('draft','issued','partially_paid','paid','cancelled')),
    issue_date          DATE,
    due_date            DATE,
    currency            CHAR(3) NOT NULL DEFAULT 'INR',
    -- Who billed whom, frozen at issue time so later profile edits don't change old invoices.
    supplier            JSONB NOT NULL DEFAULT '{}'::jsonb,   -- creator name, GSTIN, PAN, address, state
    buyer               JSONB NOT NULL DEFAULT '{}'::jsonb,   -- brand legal name, GSTIN, address, state
    place_of_supply     TEXT NOT NULL DEFAULT '',             -- state code
    tax_type            TEXT NOT NULL DEFAULT 'intra' CHECK (tax_type IN ('intra','inter','export','none')),
    reverse_charge      BOOLEAN NOT NULL DEFAULT false,
    subtotal_paise      BIGINT NOT NULL DEFAULT 0 CHECK (subtotal_paise >= 0),
    cgst_paise          BIGINT NOT NULL DEFAULT 0 CHECK (cgst_paise >= 0),
    sgst_paise          BIGINT NOT NULL DEFAULT 0 CHECK (sgst_paise >= 0),
    igst_paise          BIGINT NOT NULL DEFAULT 0 CHECK (igst_paise >= 0),
    total_paise         BIGINT NOT NULL DEFAULT 0 CHECK (total_paise >= 0),
    tds_expected_paise  BIGINT NOT NULL DEFAULT 0 CHECK (tds_expected_paise >= 0),
    amount_paid_paise   BIGINT NOT NULL DEFAULT 0 CHECK (amount_paid_paise >= 0),
    notes               TEXT NOT NULL DEFAULT '',
    pdf_storage_key     TEXT NOT NULL DEFAULT '',  -- object storage key of the rendered PDF
    issued_at           TIMESTAMPTZ,
    paid_at             TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    cancel_reason       TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (total_paise = subtotal_paise + cgst_paise + sgst_paise + igst_paise),
    CHECK (status = 'draft' OR (invoice_number IS NOT NULL AND issue_date IS NOT NULL))
);
CREATE UNIQUE INDEX invoices_number_uniq ON invoices (creator_id, invoice_number) WHERE invoice_number IS NOT NULL;
CREATE INDEX invoices_creator_status_idx ON invoices (creator_id, status, due_date);
CREATE INDEX invoices_deal_idx ON invoices (deal_id);
-- Fast lookup for the overdue-reminder job.
CREATE INDEX invoices_overdue_idx ON invoices (due_date) WHERE status IN ('issued','partially_paid');

CREATE TABLE invoice_items (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id       UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    position         INT  NOT NULL,
    description      TEXT NOT NULL,
    sac_code         TEXT NOT NULL DEFAULT '',
    quantity         NUMERIC(12,3) NOT NULL DEFAULT 1 CHECK (quantity > 0),
    unit_price_paise BIGINT NOT NULL CHECK (unit_price_paise >= 0),
    taxable_paise    BIGINT NOT NULL CHECK (taxable_paise >= 0),
    gst_rate_bp      INT    NOT NULL DEFAULT 1800 CHECK (gst_rate_bp BETWEEN 0 AND 2800), -- basis points: 1800 = 18%
    tax_paise        BIGINT NOT NULL DEFAULT 0 CHECK (tax_paise >= 0),
    deliverable_id   UUID REFERENCES deliverables(id) ON DELETE SET NULL,
    UNIQUE (invoice_id, position)
);

CREATE TABLE payment_links (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id      UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    creator_id      UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    gateway         TEXT NOT NULL,               -- e.g. 'razorpay', 'cashfree'
    gateway_link_id TEXT NOT NULL,
    url             TEXT NOT NULL,
    amount_paise    BIGINT NOT NULL CHECK (amount_paise > 0),
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paid','expired','cancelled')),
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (gateway, gateway_link_id)
);
CREATE INDEX payment_links_invoice_idx ON payment_links (invoice_id);

CREATE TABLE payments (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id         UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    creator_id         UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    amount_paise       BIGINT NOT NULL CHECK (amount_paise > 0),   -- money actually received
    tds_deducted_paise BIGINT NOT NULL DEFAULT 0 CHECK (tds_deducted_paise >= 0),
    method             TEXT NOT NULL DEFAULT 'other' CHECK (method IN
                         ('upi','card','netbanking','bank_transfer','cheque','cash','other')),
    source             TEXT NOT NULL CHECK (source IN ('gateway','manual')),
    gateway            TEXT NOT NULL DEFAULT '',
    gateway_payment_id TEXT,
    reference          TEXT NOT NULL DEFAULT '',  -- UTR / cheque no. for manual entries
    received_at        TIMESTAMPTZ NOT NULL,
    recorded_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- A gateway payment can only be recorded once, even if the webhook is retried.
CREATE UNIQUE INDEX payments_gateway_uniq ON payments (gateway, gateway_payment_id) WHERE gateway_payment_id IS NOT NULL;
CREATE INDEX payments_invoice_idx ON payments (invoice_id);
CREATE INDEX payments_creator_idx ON payments (creator_id, received_at DESC);

-- Every inbound webhook (payment gateway, Gmail push, Instagram) is stored
-- first, then processed by a worker. The unique key drops duplicates.
CREATE TABLE webhook_events (
    id           BIGSERIAL PRIMARY KEY,
    provider     TEXT NOT NULL,
    event_id     TEXT NOT NULL,
    event_type   TEXT NOT NULL DEFAULT '',
    payload      JSONB NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    attempts     INT NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    UNIQUE (provider, event_id)
);
CREATE INDEX webhook_events_pending_idx ON webhook_events (received_at) WHERE processed_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS webhook_events;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS payment_links;
DROP TABLE IF EXISTS invoice_items;
DROP TABLE IF EXISTS invoices;
DROP TABLE IF EXISTS invoice_series;
ALTER TABLE brands
    DROP COLUMN IF EXISTS legal_name, DROP COLUMN IF EXISTS address_line1, DROP COLUMN IF EXISTS address_line2,
    DROP COLUMN IF EXISTS city, DROP COLUMN IF EXISTS pincode, DROP COLUMN IF EXISTS state_code,
    DROP COLUMN IF EXISTS country;
ALTER TABLE creators
    DROP COLUMN IF EXISTS address_line1, DROP COLUMN IF EXISTS address_line2, DROP COLUMN IF EXISTS pincode,
    DROP COLUMN IF EXISTS pan, DROP COLUMN IF EXISTS default_sac, DROP COLUMN IF EXISTS lut_number;
