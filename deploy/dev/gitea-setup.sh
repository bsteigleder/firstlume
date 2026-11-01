#!/usr/bin/env bash
# Prepares the local Gitea (make dev-gitea) for developing the Git connection:
# an admin, a read-only bot with a token, the organization "acme" and
# repositories with .firstlume folders. Safe to run again. Credentials go to
# .env (git-ignored) and are never printed.
set -euo pipefail
cd "$(dirname "$0")/../.."
. deploy/dev/tg-example.sh
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

# Content of acme/infra. Rewritten on every run and committed only when it
# changed, so existing setups pick up new examples.
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
if [ "$(api "$GITEA/api/v1/repos/$ORG/infra" | jq -r .empty)" = "true" ]; then
  git init -q -b main "$WORK"
  AUTHOR=(-c user.name="Ana Souza" -c user.email=ana@example.com)
  MESSAGE="Environments prod, edge and dev"
else
  git -c http.extraHeader="$AUTH" clone -q "$GITEA/$ORG/infra.git" "$WORK"
  AUTHOR=(-c user.name="Bruno Lima" -c user.email=bruno@example.com)
  MESSAGE="Keep state in Firstlume and add a Terragrunt stack"
fi
cd "$WORK"
for env in prod edge dev; do
  mkdir -p "environments/$env/.firstlume"
  printf 'version: 1\nengine: terraform\nengine_version: 1.15.6\n' > "environments/$env/.firstlume/config.yaml"
  cat > "environments/$env/main.tf" <<TF
# The state lives in Firstlume, which fills in the address and credentials.
terraform {
  backend "http" {}
}
variable "greeting" {
  type    = string
  default = "hello"
}
resource "terraform_data" "$env" { input = "\${var.greeting} from $env" }
output "message" { value = terraform_data.$env.output }
TF
done

# A Terragrunt stack: two units, one state each, app reading net's outputs.
write_tg_units environments/tg
cat > environments/tg/root.hcl <<'HCL'
remote_state {
  backend  = "http"
  generate = { path = "backend.tf", if_exists = "overwrite_terragrunt" }
  config = {
    address        = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
    lock_address   = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
    unlock_address = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
  }
}
HCL

# Machines whose connection.hcl Firstlume writes from the declaration in
# config.yaml (spec 0003): one per machine directory, never committed.
mkdir -p environments/machines/.firstlume
cat > environments/machines/.firstlume/config.yaml <<'YAML'
version: 1
engine: terraform
engine_version: 1.15.6
terragrunt_version: 1.1.6
run_all: true
files:
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals
    values:
      host: "${name}.lan"
      user: { var: SSH_USER }
      port: 22
YAML
cp environments/tg/root.hcl environments/machines/root.hcl
echo 'connection.hcl' > environments/machines/.gitignore
for m in sp1/n01 sp1/n02 sp2/n03; do
  mkdir -p "environments/machines/$m"
  cat > "environments/machines/$m/terragrunt.hcl" <<'HCL'
include "root" { path = find_in_parent_folders("root.hcl") }
locals {
  connection = read_terragrunt_config("connection.hcl").locals
}
inputs = local.connection
HCL
  cat > "environments/machines/$m/main.tf" <<'TF'
variable "host" { type = string }
variable "user" { type = string }
variable "port" { type = number }
resource "terraform_data" "machine" { input = "${var.user}@${var.host}:${var.port}" }
TF
done

# Apply followed resource by resource (spec 0004), with no cloud: fast and
# slow resources, one that fails when the Firstlume variable fail = true, one
# that depends on it (not applied) and one recreated whenever rev changes.
mkdir -p environments/apply-demo/.firstlume
printf 'version: 1\nengine: terraform\nengine_version: 1.15.6\n' > environments/apply-demo/.firstlume/config.yaml
cat > environments/apply-demo/main.tf <<'TF'
terraform {
  backend "http" {}
}
variable "fail" {
  type    = bool
  default = false
}
variable "rev" {
  type    = string
  default = "1"
}
resource "terraform_data" "network" { input = "net-${var.rev}" }
resource "terraform_data" "dns" { input = "dns-${var.rev}" }
resource "terraform_data" "vm" {
  input = terraform_data.network.output
  provisioner "local-exec" { command = "sleep 40" }
}
resource "terraform_data" "release" {
  triggers_replace = [var.rev]
  provisioner "local-exec" {
    command = var.fail ? "echo 'timed out waiting for the condition' >&2; exit 1" : "sleep 5"
  }
}
resource "terraform_data" "release_dns" { input = terraform_data.release.id }
resource "terraform_data" "web" { triggers_replace = [var.rev] }
TF

mkdir -p legacy/vpn/.firstlume
printf 'version: 1\nengine: terraform\nengine_version: latest\n' > legacy/vpn/.firstlume/config.yaml
echo 'resource "terraform_data" "vpn" {}' > legacy/vpn/main.tf

git add -A
if ! git diff --cached --quiet; then
  git "${AUTHOR[@]}" commit -qm "$MESSAGE"
  git -c http.extraHeader="$AUTH" push -q "$GITEA/$ORG/infra.git" main
fi
cd - >/dev/null

echo "Gitea ready at $GITEA: organization $ORG, bot $BOT (token in .env as FIRSTLUME_DEV_GITEA_TOKEN)."
echo "Sign in to Gitea as $ADMIN with the password in .env (FIRSTLUME_DEV_GITEA_ADMIN_PASSWORD)."
