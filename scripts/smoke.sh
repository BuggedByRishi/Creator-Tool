#!/usr/bin/env bash
# End-to-end smoke test against a running API. Usage: ./scripts/smoke.sh [base_url]
set -euo pipefail
BASE=${1:-http://localhost:8080}/api/v1
JAR=$(mktemp); trap 'rm -f "$JAR"' EXIT
EMAIL="smoke+$(date +%s)@example.com"

call() { # method path [json]
  local m=$1 p=$2 d=${3:-}
  if [[ -n $d ]]; then
    curl -sS -b "$JAR" -c "$JAR" -X "$m" -H 'Content-Type: application/json' -d "$d" -w '\n%{http_code}' "$BASE$p"
  else
    curl -sS -b "$JAR" -c "$JAR" -X "$m" -w '\n%{http_code}' "$BASE$p"
  fi
}
expect() { # want_status label response
  local code=${3##*$'\n'} body=${3%$'\n'*}
  if [[ $code != "$1" ]]; then echo "FAIL $2: got $code, want $1: $body"; exit 1; fi
  echo "ok   $2 ($code)"; LAST=$body
}
field() { python3 -c "import sys,json; print(json.load(sys.stdin)$1)" <<<"$LAST"; }

expect 401 "me without session"   "$(call GET /me)"
expect 201 "signup"               "$(call POST /auth/signup "{\"email\":\"$EMAIL\",\"password\":\"correct-horse-1\",\"name\":\"Rishi\",\"display_name\":\"Rishi Builds\"}")"
expect 409 "duplicate signup"     "$(call POST /auth/signup "{\"email\":\"$EMAIL\",\"password\":\"correct-horse-1\",\"name\":\"x\"}")"
expect 200 "me"                   "$(call GET /me)"
expect 400 "bad GSTIN"            "$(call PATCH /creator '{"gstin":"27AAAAA0000A1Z0"}')"
expect 200 "update creator"       "$(call PATCH /creator '{"niche":"tech","city":"Pune","gstin":"27AAPFU0939F1ZV"}')"
[[ $(field "['state_code']") == 27 ]] && echo "ok   state code derived from GSTIN"
expect 415 "form post rejected"   "$(curl -sS -b "$JAR" -X POST -d 'name=x' -w '\n%{http_code}' "$BASE/brands")"

expect 201 "create brand"         "$(call POST /brands '{"name":"boAt","website":"https://www.boat-lifestyle.com"}')"
BRAND=$(field "['id']")
[[ $(field "['domain']") == boat-lifestyle.com ]] && echo "ok   domain extracted"
expect 409 "duplicate brand"      "$(call POST /brands '{"name":"BOAT"}')"
expect 201 "add contact"          "$(call POST /brands/$BRAND/contacts '{"name":"Priya","email":"Priya@Boat.com","role":"Influencer manager"}')"

expect 201 "create deal"          "$(call POST /deals "{\"title\":\"Airdopes launch reel\",\"brand_id\":\"$BRAND\",\"amount_paise\":4500000,\"usage_rights\":\"3 months organic\",\"exclusivity_days\":30}")"
DEAL=$(field "['id']")
expect 400 "deal with foreign brand" "$(call POST /deals '{"title":"x","brand_id":"00000000-0000-0000-0000-000000000001"}')"
expect 201 "add deliverable"      "$(call POST /deals/$DEAL/deliverables '{"platform":"instagram","format":"reel","due_at":"2026-11-05T12:00:00+05:30"}')"
DEL=$(field "['id']")
expect 201 "add deliverable 2"    "$(call POST /deals/$DEAL/deliverables '{"platform":"instagram","format":"story"}')"
expect 200 "upcoming deliverables" "$(call GET '/deliverables/upcoming?days=30')"
expect 200 "move to negotiating"  "$(call POST /deals/$DEAL/stage '{"stage":"negotiating"}')"
expect 200 "move to contracted"   "$(call POST /deals/$DEAL/stage '{"stage":"contracted"}')"
expect 200 "mark posted"          "$(call PATCH /deliverables/$DEL '{"status":"posted","post_url":"https://instagram.com/reel/abc"}')"
expect 200 "deal detail"          "$(call GET /deals/$DEAL)"
[[ $(field "['deliverables_done']") == 1 && $(field "['deliverables_total']") == 2 ]] && echo "ok   deliverable counts"
expect 200 "pipeline"             "$(call GET /deals/pipeline)"
[[ $(field "['open_pipeline_paise']") == 4500000 ]] && echo "ok   open pipeline = ₹45,000"
expect 200 "stage history"        "$(call GET /deals/$DEAL/history)"
expect 200 "brand lifetime"       "$(call POST /deals/$DEAL/stage '{"stage":"paid"}')"
expect 200 "brand detail"         "$(call GET /brands/$BRAND)"
[[ $(field "['paid_paise']") == 4500000 ]] && echo "ok   brand paid total"
expect 204 "logout"               "$(call POST /auth/logout)"
expect 401 "me after logout"      "$(call GET /me)"
echo "ALL PASSED"
