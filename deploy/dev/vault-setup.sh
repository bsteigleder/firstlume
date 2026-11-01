#!/usr/bin/env bash
# Prepares a local Vault (dev mode) for the Vault sources of spec 0003: the
# jwt auth method trusting the local Firstlume's run JWTs, example secrets
# under secret/infra/{dev,prod}, and the roles dev and prod, each bound to the
# stacks under a folder of that name. Needs the dev server running (make
# run-server), whose public key Vault is given. Safe to run again. The root
# token goes to .env (git-ignored) and is never printed.
set -euo pipefail
cd "$(dirname "$0")/../.."
touch .env
set -a; . ./.env; set +a

save() { # save KEY VALUE into .env, replacing an older value
  grep -v "^$1=" .env > .env.tmp || true
  echo "$1=$2" >> .env.tmp
  mv .env.tmp .env
}

if [ -z "${FIRSTLUME_DEV_VAULT_ROOT_TOKEN:-}" ]; then
  FIRSTLUME_DEV_VAULT_ROOT_TOKEN=$(openssl rand -hex 16)
  save FIRSTLUME_DEV_VAULT_ROOT_TOKEN "$FIRSTLUME_DEV_VAULT_ROOT_TOKEN"
fi
VAULT=http://127.0.0.1:8200
save FIRSTLUME_DEV_VAULT_ADDR "$VAULT"
docker compose --profile vault up -d --wait vault

FIRSTLUME=${FIRSTLUME_DEV_URL:-http://127.0.0.1:8787}
ADMIN="Authorization: Bearer ${FIRSTLUME_ADMIN_TOKEN:?run make dev-env first}"
if ! PEM=$(curl -sfS -H "$ADMIN" "$FIRSTLUME/api/v1/orgs/default/vault/public-key"); then
  echo "The Firstlume dev server must be running at $FIRSTLUME (make run-server)." >&2
  exit 1
fi

BODY=$(mktemp)
trap 'rm -f "$BODY"' EXIT
vault() { # vault METHOD PATH [JSON]: prints the status; Vault's errors go to stderr
  local args=(-sS -o "$BODY" -w '%{http_code}' -X "$1" -H "X-Vault-Token: $FIRSTLUME_DEV_VAULT_ROOT_TOKEN")
  if [ $# -ge 3 ]; then
    args+=(-H "Content-Type: application/json" -d "$3")
  fi
  local code
  code=$(curl "${args[@]}" "$VAULT/v1/$2")
  case "$code" in 2*) ;; *) echo "$1 $2: $(jq -c .errors "$BODY" 2>/dev/null)" >&2 ;; esac
  echo "$code"
}
ok() { case "$1" in 2*) ;; *) echo "Vault answered $1 when $2" >&2; exit 1 ;; esac; }

[ "$(vault GET sys/auth/jwt)" = 200 ] || ok "$(vault POST sys/auth/jwt '{"type": "jwt"}')" "enabling jwt"
ok "$(vault POST auth/jwt/config "$(jq -n --arg pem "$PEM" --arg iss "$FIRSTLUME" \
  '{jwt_validation_pubkeys: [$pem], bound_issuer: $iss}')")" "configuring jwt"

for env in dev prod; do
  for n in n01 n02 n03; do
    secret=$(jq -n --arg ip "10.$([ "$env" = dev ] && echo 1 || echo 2).0.${n#n0}" --arg user "ops-$env" '{data: {ip: $ip, user: $user}}')
    ok "$(vault POST "secret/data/infra/$env/$n" "$secret")" "writing a secret"
  done
  ok "$(vault PUT "sys/policies/acl/firstlume-$env" \
    "$(jq -n --arg p "path \"secret/data/infra/$env/*\" { capabilities = [\"read\"] }" '{policy: $p}')")" "writing a policy"
  ok "$(vault POST "auth/jwt/role/$env" "$(jq -n --arg env "$env" '{
    role_type: "jwt", user_claim: "sub", bound_audiences: ["vault"],
    bound_claims_type: "glob", bound_claims: {stack_path: [($env + "/*"), ("*/" + $env + "/*")]},
    token_policies: ["firstlume-" + $env], token_ttl: "15m", token_max_ttl: "1h"}')")" "writing a role"
done

ok "$(curl -sS -o /dev/null -w '%{http_code}' -X PUT -H "$ADMIN" -H "Content-Type: application/json" \
  -d "{\"address\": \"$VAULT\"}" "$FIRSTLUME/api/v1/orgs/default/vault")" "setting Firstlume's Vault connection"

echo "Vault ready at $VAULT: roles dev and prod for stacks under a folder dev or prod, secrets in secret/infra/{dev,prod}/n0{1,2,3}."
echo "The organization default uses it; set vault_role on a folder to dev or prod."
