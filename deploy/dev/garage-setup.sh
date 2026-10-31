#!/usr/bin/env bash
# Prepares a local Garage (S3-compatible) as Firstlume's state storage: a
# single-node layout, the buckets firstlume-state (dev server) and
# firstlume-test (integration tests) and an access key for both. Safe to run
# again. Settings go to .env (git-ignored); the secret is never printed.
set -euo pipefail
cd "$(dirname "$0")/../.."
touch .env
set -a; . ./.env; set +a

save() { # save KEY VALUE into .env, replacing an older value
  grep -v "^$1=" .env > .env.tmp || true
  echo "$1=$2" >> .env.tmp
  mv .env.tmp .env
}
garage() { docker compose --profile garage exec -T garage /garage "$@" 2>/dev/null; }

if [ -z "${FIRSTLUME_DEV_GARAGE_RPC_SECRET:-}" ]; then
  save FIRSTLUME_DEV_GARAGE_RPC_SECRET "$(openssl rand -hex 32)"
fi
docker compose --profile garage up -d --wait garage

# One node holding everything.
if garage layout show | grep -q "Current cluster layout version: 0"; then
  node=$(garage node id -q | cut -d@ -f1)
  garage layout assign -z dev -c 1G "$node" >/dev/null
  garage layout apply --version 1 >/dev/null
fi

for bucket in firstlume-state firstlume-test; do
  garage bucket create "$bucket" >/dev/null || true
done

key=${FIRSTLUME_STATE_S3_ACCESS_KEY_ID:-}
if [ -z "$key" ] || ! garage key info "$key" >/dev/null; then
  out=$(garage key create firstlume-dev)
  key=$(echo "$out" | awk -F': *' '/^Key ID/ {print $2}')
  save FIRSTLUME_STATE_S3_ACCESS_KEY_ID "$key"
  save FIRSTLUME_STATE_S3_SECRET_ACCESS_KEY "$(echo "$out" | awk -F': *' '/^Secret key/ {print $2}')"
fi
for bucket in firstlume-state firstlume-test; do
  garage bucket allow --read --write --owner "$bucket" --key "$key" >/dev/null
done

save FIRSTLUME_STATE_STORAGE s3
save FIRSTLUME_STATE_S3_ENDPOINT http://127.0.0.1:3900
save FIRSTLUME_STATE_S3_BUCKET firstlume-state
save FIRSTLUME_STATE_S3_REGION garage
save FIRSTLUME_DEV_GARAGE_TEST_BUCKET firstlume-test

echo "Garage ready at http://127.0.0.1:3900: buckets firstlume-state and firstlume-test, key $key (secret in .env)."
echo "The server now keeps Terraform state in Garage (FIRSTLUME_STATE_STORAGE=s3 in .env)."
