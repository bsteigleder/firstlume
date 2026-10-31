#!/usr/bin/env bash
# Generates deploy/dev/state-bundle.tar.gz: the states of the Terragrunt
# example stack (acme/infra environments/tg) as a CI pipeline without a state
# backend keeps them, a local state per unit bundled after `run --all apply`.
# Import it with the prefix environments/tg (spec 0002, R9).
set -euo pipefail
cd "$(dirname "$0")/../.."
. deploy/dev/tg-example.sh
TERRAGRUNT=${TERRAGRUNT:-terragrunt}
OUT=$PWD/deploy/dev/state-bundle.tar.gz
# The real path: on macOS the temporary directory is behind a symbolic link,
# and Terragrunt would compute different state paths through each.
WORK=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$WORK"' EXIT

write_tg_units "$WORK/environments/tg"
cat > "$WORK/environments/tg/root.hcl" <<'HCL'
remote_state {
  backend  = "local"
  generate = { path = "backend.tf", if_exists = "overwrite_terragrunt" }
  config   = { path = "${get_parent_terragrunt_dir()}/${path_relative_to_include()}/terraform.tfstate" }
}
HCL
if ! (cd "$WORK/environments/tg" && "$TERRAGRUNT" run --all --non-interactive -- apply -input=false -auto-approve) \
  >"$WORK/apply.log" 2>&1; then
  cat "$WORK/apply.log"
  exit 1
fi
(cd "$WORK" && COPYFILE_DISABLE=1 tar -czf "$OUT" $(find environments -name terraform.tfstate -not -path '*/.terragrunt-cache/*' | sort))
echo "Wrote $OUT:"
tar -tzf "$OUT"
