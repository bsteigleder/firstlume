#!/usr/bin/env bash
# Prepares a local OCI registry with a login for the registry credentials of
# spec 0003: a user with a random password (in .env, never printed) and the
# chart oci://localhost:5001/charts/hello:0.1.0. Safe to run again.
set -euo pipefail
cd "$(dirname "$0")/../.."
touch .env
set -a; . ./.env; set +a

save() { # save KEY VALUE into .env, replacing an older value
  grep -v "^$1=" .env > .env.tmp || true
  echo "$1=$2" >> .env.tmp
  mv .env.tmp .env
}

USER_NAME=firstlume-dev
if [ -z "${FIRSTLUME_DEV_REGISTRY_PASSWORD:-}" ]; then
  FIRSTLUME_DEV_REGISTRY_PASSWORD=$(openssl rand -hex 16)
  save FIRSTLUME_DEV_REGISTRY_PASSWORD "$FIRSTLUME_DEV_REGISTRY_PASSWORD"
fi
save FIRSTLUME_DEV_REGISTRY localhost:5001
save FIRSTLUME_DEV_REGISTRY_USER "$USER_NAME"
docker run --rm --entrypoint htpasswd httpd:2.4-alpine -Bbn "$USER_NAME" "$FIRSTLUME_DEV_REGISTRY_PASSWORD" \
  > deploy/dev/registry.htpasswd 2>/dev/null
docker compose --profile registry up -d --force-recreate registry >/dev/null
until curl -s -o /dev/null -w '%{http_code}' http://localhost:5001/v2/ | grep -q 401; do sleep 1; done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/hello/templates"
printf 'apiVersion: v2\nname: hello\nversion: 0.1.0\n' > "$WORK/hello/Chart.yaml"
printf 'greeting: hello\n' > "$WORK/hello/values.yaml"
cat > "$WORK/hello/templates/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  greeting: {{ .Values.greeting | quote }}
YAML
helm package "$WORK/hello" -d "$WORK" >/dev/null
# A login of its own, so the user's Helm configuration is left alone.
export HELM_REGISTRY_CONFIG="$WORK/registry.json"
echo "$FIRSTLUME_DEV_REGISTRY_PASSWORD" | helm registry login localhost:5001 --plain-http -u "$USER_NAME" --password-stdin >/dev/null
helm push "$WORK/hello-0.1.0.tgz" oci://localhost:5001/charts --plain-http >/dev/null 2>&1
echo "Registry ready at localhost:5001 (user $USER_NAME, password in .env as FIRSTLUME_DEV_REGISTRY_PASSWORD) with oci://localhost:5001/charts/hello:0.1.0."
