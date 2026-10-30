#!/usr/bin/env bash
# Creates a folder and a stack for examples/hello (cloned from GitHub) in the
# default organization and starts an apply run, following its logs. Needs
# `make run-server` and `make run-agent` running. Usage: deploy/dev/demo.sh [greeting]
set -euo pipefail
cd "$(dirname "$0")/../.."
set -a; . ./.env; set +a

API=${FIRSTLUME_API:-http://127.0.0.1:8787}
O="$API/api/v1/orgs/${FIRSTLUME_ORGANIZATION:-default}"
H="Authorization: Bearer $FIRSTLUME_ADMIN_TOKEN"
GREETING=${1:-"olá do Firstlume"}

api() { curl -sf -H "$H" -H "Content-Type: application/json" "$@"; }

FOLDER=$(api "$O/folders" -d '{"name": "demo-'$RANDOM'"}' | jq -r .id)
api -X PUT "$O/folders/$FOLDER/settings" -d "$(jq -n --arg g "$GREETING" \
  '{variables: [{key: "greeting", category: "terraform", sensitive: true, value: $g}]}')" > /dev/null

STACK=$(api "$O/stacks" -d '{
  "folder_id": "'$FOLDER'",
  "name": "hello",
  "repository": "https://github.com/bsteigleder/firstlume.git",
  "path": "examples/hello"
}' | jq -r .id)

RUN=$(api "$O/stacks/$STACK/runs" -d '{"type": "apply"}' | jq -r .id)

echo "folder: $FOLDER"
echo "stack:  $STACK"
echo "run:    $RUN"
echo
echo "--- logs (plan) ---"
curl -sN -H "$H" "$O/runs/$RUN/logs?follow=true" \
  | sed -n 's/^data: //p' | jq -rj 'if .data then .data else "\n[status: \(.status)]\n" end'

cat <<NEXT

Next steps:
  changes:  curl -s -H "Authorization: Bearer \$FIRSTLUME_ADMIN_TOKEN" $O/runs/$RUN/changes | jq .
  approve:  curl -s -X POST -H "Authorization: Bearer \$FIRSTLUME_ADMIN_TOKEN" $O/runs/$RUN/approve -d '{}' | jq .status
  discard:  curl -s -X POST -H "Authorization: Bearer \$FIRSTLUME_ADMIN_TOKEN" $O/runs/$RUN/discard | jq .status
NEXT
