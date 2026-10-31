#!/usr/bin/env bash
# Prepares the local Gitea (make dev-gitea) for developing the Git connection:
# an admin, a read-only bot with a token, the organization "acme" and
# repositories with .firstlume folders. Safe to run again. Credentials go to
# .env (git-ignored) and are never printed.
set -euo pipefail
cd "$(dirname "$0")/../.."
touch .env
set -a; . ./.env; set +a

GITEA=${FIRSTLUME_DEV_GITEA_URL:-http://127.0.0.1:3000}
ADMIN=devadmin
BOT=firstlume-bot
ORG=acme

save() { # save KEY VALUE into .env, replacing an older value
  grep -v "^$1=" .env > .env.tmp || true
  echo "$1=$2" >> .env.tmp
  mv .env.tmp .env
}
gitea() { docker compose --profile gitea exec -T -u git gitea gitea "$@"; }

if [ -z "${FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD:-}" ]; then
  FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD=$(openssl rand -hex 16)
  save FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD "$FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD"
fi
PW=$FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD
AUTH="Authorization: Basic $(printf '%s:%s' "$ADMIN" "$PW" | base64)"
api() { curl -sS -H "$AUTH" -H "Content-Type: application/json" "$@"; }

gitea admin user create --admin --username "$ADMIN" --password "$PW" --email devadmin@example.com \
  --must-change-password=false >/dev/null 2>&1 || true
gitea admin user create --username "$BOT" --random-password --email bot@example.com \
  --must-change-password=false >/dev/null 2>&1 || true

api -o /dev/null -X POST "$GITEA/api/v1/orgs" -d '{"username": "'$ORG'", "visibility": "private"}' || true

# Repositories: infra holds the stacks; a few more show up in the repository list.
for repo in infra network dns storage; do
  api -o /dev/null -X POST "$GITEA/api/v1/orgs/$ORG/repos" -d '{"name": "'$repo'", "private": true, "default_branch": "main"}' || true
done

# Read-only access for the bot to every repository of the organization.
TEAM=$(api "$GITEA/api/v1/orgs/$ORG/teams" | jq -r '.[] | select(.name=="readers") | .id')
if [ -z "$TEAM" ]; then
  TEAM=$(api -X POST "$GITEA/api/v1/orgs/$ORG/teams" -d '{"name": "readers", "permission": "read", "includes_all_repositories": true,
    "units": ["repo.code"]}' | jq -r .id)
fi
api -o /dev/null -X PUT "$GITEA/api/v1/teams/$TEAM/members/$BOT"

if [ -z "${FIRSTLUME_DEV_GITEA_TOKEN:-}" ]; then
  TOKEN=$(gitea admin user generate-access-token --username "$BOT" --token-name "firstlume-$(date +%s)" \
    --scopes "read:repository,read:organization,read:user" --raw)
  save FIRSTLUME_DEV_GITEA_TOKEN "$TOKEN"
fi
save FIRSTLUME_DEV_GITEA_URL "$GITEA"
save FIRSTLUME_DEV_GITEA_ACCOUNT "$ORG"

# Content of acme/infra, pushed once.
if [ "$(api "$GITEA/api/v1/repos/$ORG/infra" | jq -r .empty)" = "true" ]; then
  WORK=$(mktemp -d)
  trap 'rm -rf "$WORK"' EXIT
  cd "$WORK"
  git init -q -b main
  for env in prod edge dev; do
    mkdir -p "environments/$env/.firstlume"
    printf 'version: 1\nengine: terraform\nengine_version: 1.15.6\n' > "environments/$env/.firstlume/config.yaml"
    cat > "environments/$env/main.tf" <<TF
variable "greeting" {
  type    = string
  default = "hello"
}
resource "terraform_data" "$env" { input = "\${var.greeting} from $env" }
output "message" { value = terraform_data.$env.output }
TF
  done
  mkdir -p legacy/vpn/.firstlume
  printf 'version: 1\nengine: terraform\nengine_version: latest\n' > legacy/vpn/.firstlume/config.yaml
  echo 'resource "terraform_data" "vpn" {}' > legacy/vpn/main.tf
  git add -A
  git -c user.name="Ana Souza" -c user.email=ana@example.com commit -qm "Environments prod, edge and dev"
  git -c http.extraHeader="$AUTH" push -q "$GITEA/$ORG/infra.git" main
  cd - >/dev/null
fi

echo "Gitea ready at $GITEA: organization $ORG, bot $BOT (token in .env as FIRSTLUME_DEV_GITEA_TOKEN)."
echo "Sign in to Gitea as $ADMIN with the password in .env (FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD)."
