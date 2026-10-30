#!/usr/bin/env bash
# Creates a template and environment for examples/hello (cloned from GitHub)
# and starts an apply run, following its logs. Needs `make run-server` and
# `make run-agent` running. Usage: deploy/dev/demo.sh [greeting]
set -euo pipefail
cd "$(dirname "$0")/../.."
set -a; . ./.env; set +a

API=${FIRSTLUME_API:-http://127.0.0.1:8787}
H="Authorization: Bearer $FIRSTLUME_ADMIN_TOKEN"
GREETING=${1:-"olá do Firstlume"}

api() { curl -sf -H "$H" -H "Content-Type: application/json" "$@"; }

TPL=$(api "$API/api/v1/templates" -d '{
  "name": "hello-'$RANDOM'",
  "repository": "https://github.com/bsteigleder/firstlume.git",
  "path": "examples/hello",
  "engine": "terraform",
  "engine_version": "1.15.6"
}' | jq -r .id)

ENV=$(api "$API/api/v1/environments" -d '{"template_id": "'$TPL'", "name": "demo-'$RANDOM'"}' | jq -r .id)

api -X PUT "$API/api/v1/environments/$ENV/variables" -d "$(jq -n --arg g "$GREETING" \
  '{variables: [{key: "greeting", category: "terraform", sensitive: true, value: $g}]}')" > /dev/null

RUN=$(api "$API/api/v1/environments/$ENV/runs" -d '{"type": "apply"}' | jq -r .id)

echo "template:    $TPL"
echo "environment: $ENV"
echo "run:         $RUN"
echo
echo "--- logs (plan) ---"
curl -sN -H "$H" "$API/api/v1/runs/$RUN/logs?follow=true" \
  | sed -n 's/^data: //p' | jq -rj 'if .data then .data else "\n[status: \(.status)]\n" end'

cat <<EOF

Next steps:
  approve:  curl -s -X POST -H "Authorization: Bearer \$FIRSTLUME_ADMIN_TOKEN" $API/api/v1/runs/$RUN/approve | jq .status
  discard:  curl -s -X POST -H "Authorization: Bearer \$FIRSTLUME_ADMIN_TOKEN" $API/api/v1/runs/$RUN/discard | jq .status
  logs:     curl -sN -H "Authorization: Bearer \$FIRSTLUME_ADMIN_TOKEN" "$API/api/v1/runs/$RUN/logs?follow=true"
EOF
