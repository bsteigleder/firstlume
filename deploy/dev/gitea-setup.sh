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

# Sign-in with Gitea (spec 0001, R7.13): an OAuth app for the dev server, a
# team acme:infra and a user outside acme, to try the allowed groups (R7.11).
# devadmin is in acme (Owners) and acme:infra; outsider is in neither.
PUBLIC_URL=${FIRSTLUME_PUBLIC_URL:-http://127.0.0.1:8787}
INFRA=$(api "$GITEA/api/v1/orgs/$ORG/teams" | jq -r '.[] | select(.name=="infra") | .id')
if [ -z "$INFRA" ]; then
  INFRA=$(api -X POST "$GITEA/api/v1/orgs/$ORG/teams" -d '{"name": "infra", "permission": "read", "includes_all_repositories": true,
    "units": ["repo.code"]}' | jq -r .id)
fi
api -o /dev/null -X PUT "$GITEA/api/v1/teams/$INFRA/members/$ADMIN"
if [ -z "${FIRSTLUME_DEV_GITEA_OUTSIDER_PASSWORD:-}" ]; then
  FIRSTLUME_DEV_GITEA_OUTSIDER_PASSWORD=$(openssl rand -hex 16)
  save FIRSTLUME_DEV_GITEA_OUTSIDER_PASSWORD "$FIRSTLUME_DEV_GITEA_OUTSIDER_PASSWORD"
fi
gitea admin user create --username outsider --password "$FIRSTLUME_DEV_GITEA_OUTSIDER_PASSWORD" --email outsider@example.com \
  --must-change-password=false >/dev/null 2>&1 || true
# The client secret is shown only when the app is created, so the app is
# created again when .env lost it or the app is gone.
if [ -z "${FIRSTLUME_OIDC_CLIENT_ID:-}" ] || \
   ! api "$GITEA/api/v1/user/applications/oauth2" | jq -e --arg id "$FIRSTLUME_OIDC_CLIENT_ID" '.[] | select(.client_id==$id)' >/dev/null; then
  APP=$(api -X POST "$GITEA/api/v1/user/applications/oauth2" -d '{"name": "Firstlume (dev)", "confidential_client": true,
    "redirect_uris": ["'"$PUBLIC_URL"'/auth/callback"]}')
  save FIRSTLUME_OIDC_CLIENT_ID "$(jq -r .client_id <<<"$APP")"
  save FIRSTLUME_OIDC_CLIENT_SECRET "$(jq -r .client_secret <<<"$APP")"
fi
save FIRSTLUME_PUBLIC_URL "$PUBLIC_URL"
save FIRSTLUME_OIDC_ISSUER "$GITEA"
save FIRSTLUME_OIDC_ALLOWED_GROUPS "${FIRSTLUME_OIDC_ALLOWED_GROUPS:-$ORG}"
save FIRSTLUME_OIDC_ADMIN_GROUPS "${FIRSTLUME_OIDC_ADMIN_GROUPS:-$ORG:infra}"

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
# config.yaml (spec 0003): one per machine directory, never committed, with
# values from the dev Vault (make dev-vault). Put the stack in a folder named
# dev, whose vault_role is dev.
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
      host: { vault: "secret/infra/dev/${name}", field: ip }
      user: { vault: "secret/infra/dev/${name}", field: user }
      port: 22
env:
  DEPLOY_USER: { vault: secret/infra/dev/n01, field: user }
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

# A chart from the dev registry (make dev-registry), which needs a login: the
# credential comes from the registries in Firstlume's settings.
mkdir -p environments/charts/.firstlume
printf 'version: 1\nengine: terraform\nengine_version: 1.15.6\n' > environments/charts/.firstlume/config.yaml
cat > environments/charts/main.tf <<'TF'
terraform {
  backend "http" {}
  required_providers {
    helm = { source = "hashicorp/helm", version = "~> 3.1" }
  }
}
provider "helm" {}
data "helm_template" "hello" {
  name       = "hello"
  repository = "oci://localhost:5001/charts"
  chart      = "hello"
  version    = "0.1.0"
}
resource "terraform_data" "rendered" { input = sha256(data.helm_template.hello.manifest) }
TF

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

# The same in Terragrunt run --all: net and app share an address, net fails
# when fail = true, and web depends on net, so it does not run then.
mkdir -p environments/apply-demo-tg/.firstlume
printf 'version: 1\nengine: terraform\nengine_version: 1.15.6\nterragrunt_version: 1.1.6\nrun_all: true\n' \
  > environments/apply-demo-tg/.firstlume/config.yaml
cp environments/tg/root.hcl environments/apply-demo-tg/root.hcl
for u in net app web; do
  mkdir -p "environments/apply-demo-tg/$u"
  printf 'include "root" { path = find_in_parent_folders("root.hcl") }\n' > "environments/apply-demo-tg/$u/terragrunt.hcl"
done
printf 'dependencies { paths = ["../net"] }\n' >> environments/apply-demo-tg/web/terragrunt.hcl
cat > environments/apply-demo-tg/net/main.tf <<'TF'
variable "fail" {
  type    = bool
  default = false
}
resource "terraform_data" "shared" { input = "net" }
resource "terraform_data" "gateway" {
  provisioner "local-exec" { command = var.fail ? "echo 'gateway unreachable' >&2; exit 1" : "sleep 20" }
}
TF
cat > environments/apply-demo-tg/app/main.tf <<'TF'
resource "terraform_data" "shared" { input = "app" }
resource "terraform_data" "worker" {
  provisioner "local-exec" { command = "sleep 10" }
}
TF
echo 'resource "terraform_data" "site" { input = "web" }' > environments/apply-demo-tg/web/main.tf

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
echo "Sign-in with Gitea is on for the dev server: only members of ${FIRSTLUME_OIDC_ALLOWED_GROUPS:-$ORG} get in;"
echo "\"outsider\" (password FIRSTLUME_DEV_GITEA_OUTSIDER_PASSWORD) shows the refusal."
echo "Members of ${FIRSTLUME_OIDC_ADMIN_GROUPS:-$ORG:infra} see every organization; everyone else needs an invitation."
