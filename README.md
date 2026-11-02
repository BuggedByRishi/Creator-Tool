# CreatorHQ (working name) — backend

Back office for Indian creators: AI inbox → deal tracker → GST invoices → profit.
This repo is the Go modular monolith from the product spec. **Status: weeks 1–2 done** (auth, creator profile, schema, brands, deal pipeline, deliverables).

## Run it

```bash
cp .env.example .env
go mod tidy          # resolves go.sum on your machine
make up              # Postgres + Redis in Docker
make run             # API on :8080, migrates on start
make smoke           # end-to-end test (in another terminal)
```

## Layout

```
cmd/api/              entrypoint: config, DB, router, graceful shutdown
internal/config       env config
internal/db           pgxpool + embedded goose-compatible migrator
internal/httpx        JSON helpers, errors, CORS, CSRF guard, logging
internal/auth         signup/login/logout, cookie sessions, workspace + roles, GSTIN check
internal/audit        append-only audit log
internal/brands       brand book + contacts
internal/deals        pipeline, stage moves + history, deliverables
migrations/           SQL (works with the goose CLI too)
scripts/smoke.sh      end-to-end API test, also run in CI
```

Each module owns its tables and handlers; modules talk through the DB or plain Go calls. Split a module out only when load or team size demands it.

## API (v1)

All routes are under `/api/v1`. Auth is an HttpOnly session cookie. Mutations must send `Content-Type: application/json` (CSRF guard). Money is always integer **paise** (`4500000` = ₹45,000).

| Method | Path | Notes |
|---|---|---|
| POST | `/auth/signup` | `email, password (10–72), name, display_name` — creates user + creator workspace (owner) |
| POST | `/auth/login` · `/auth/logout` | |
| GET | `/me` | user, active creator, role |
| PATCH | `/creator` | profile; GSTIN validated, state code derived from it |
| GET/POST | `/brands` | `?q=` search; brand returns `deal_count`, `paid_paise` |
| GET/PATCH/DELETE | `/brands/{id}` | website → `domain` (used later to match emails) |
| GET/POST | `/brands/{id}/contacts` · DELETE `/brands/{id}/contacts/{contactID}` | |
| GET/POST | `/deals` | `?stage= &brand_id= &q=` |
| GET | `/deals/pipeline` | kanban columns with counts, totals, open pipeline value |
| GET/PATCH/DELETE | `/deals/{id}` | |
| POST | `/deals/{id}/stage` | `{stage, lost_reason}` — records history; `paid`/`lost` set `closed_at` |
| GET | `/deals/{id}/history` | stage moves |
| GET/POST | `/deals/{id}/deliverables` | platform, format, due_at, status |
| PATCH/DELETE | `/deliverables/{id}` | setting `status: posted` stamps `posted_at` |
| GET | `/deliverables/upcoming?days=7` | due-soon list |

Stages: `inquiry → negotiating → contracted → in_production → posted → invoiced → paid`, plus `lost`.
Roles: `owner`, `manager`, `editor` can write; `viewer` is read-only.

## Next (weeks 3–5)

1. `cmd/worker` with Asynq on Redis; move session cleanup there.
2. Email-forwarding inbox (`deals+<creator>@yourdomain`) — ships without Google's restricted-scope review.
3. Gmail OAuth + Pub/Sub push in parallel; tokens in KMS/Vault, not Postgres.
4. `email_threads`, `email_messages`, `ai_actions` tables; LLM classify + extract → draft deal.
