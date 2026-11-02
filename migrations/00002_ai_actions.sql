-- +goose Up
-- Every AI call is recorded: what it was asked, what it produced, and what the
-- creator did with it. Used for the audit trail, quality tracking
-- ("AI reply acceptance rate") and cost control.

CREATE TABLE ai_actions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id     UUID NOT NULL REFERENCES creators(id) ON DELETE CASCADE,
    user_id        UUID REFERENCES users(id) ON DELETE SET NULL,   -- NULL when a background job triggered it
    kind           TEXT NOT NULL CHECK (kind IN
                     ('classify_email','extract_deal','draft_reply','rate_suggestion','scam_check','clause_review')),
    model          TEXT NOT NULL,                 -- provider/model id used for the call
    prompt_version TEXT NOT NULL,                 -- bump when a prompt changes, to compare versions
    input_entity   TEXT NOT NULL,                 -- e.g. 'email_message'
    input_id       UUID,                          -- id of that row; no FK so it works for any entity
    output         JSONB NOT NULL DEFAULT '{}'::jsonb,
    outcome        TEXT NOT NULL DEFAULT 'pending' CHECK (outcome IN
                     ('pending','accepted','edited','rejected','failed')),
    error          TEXT NOT NULL DEFAULT '',
    input_tokens   INT NOT NULL DEFAULT 0,
    output_tokens  INT NOT NULL DEFAULT 0,
    latency_ms     INT NOT NULL DEFAULT 0,
    cost_micros    BIGINT NOT NULL DEFAULT 0,     -- cost in millionths of a USD
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at    TIMESTAMPTZ                    -- when the creator accepted/edited/rejected it
);
CREATE INDEX ai_actions_creator_idx ON ai_actions (creator_id, created_at DESC);
CREATE INDEX ai_actions_input_idx   ON ai_actions (input_entity, input_id);
CREATE INDEX ai_actions_quality_idx ON ai_actions (kind, prompt_version, outcome);

-- +goose Down
DROP TABLE IF EXISTS ai_actions;
