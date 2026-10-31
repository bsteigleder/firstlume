# Sourced by gitea-setup.sh and state-bundle.sh: the units of the Terragrunt
# example stack (acme/infra environments/tg). Both must write the same code,
# or an imported bundle would not plan with "No changes".

# write_tg_units DIR writes the stack's config and its units net and app;
# the caller writes DIR/root.hcl.
write_tg_units() {
  local dir=$1
  mkdir -p "$dir/.firstlume" "$dir/net" "$dir/app"
  printf 'version: 1\nengine: terraform\nengine_version: 1.15.6\nterragrunt_version: 1.1.6\nrun_all: true\n' \
    > "$dir/.firstlume/config.yaml"
  echo 'include "root" { path = find_in_parent_folders("root.hcl") }' > "$dir/net/terragrunt.hcl"
  cat > "$dir/net/main.tf" <<'TF'
resource "terraform_data" "vpc" { input = "10.0.0.0/16" }
output "cidr" { value = terraform_data.vpc.output }
TF
  cat > "$dir/app/terragrunt.hcl" <<'HCL'
include "root" { path = find_in_parent_folders("root.hcl") }
dependency "net" {
  config_path                             = "../net"
  mock_outputs                            = { cidr = "10.0.0.0/16" }
  mock_outputs_allowed_terraform_commands = ["plan", "validate"]
}
inputs = { cidr = dependency.net.outputs.cidr }
HCL
  cat > "$dir/app/main.tf" <<'TF'
variable "cidr" { type = string }
resource "terraform_data" "app" { input = "app in ${var.cidr}" }
TF
}
