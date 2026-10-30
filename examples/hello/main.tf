# Minimal configuration for trying Firstlume: terraform_data is built into
# Terraform, so no provider needs to be downloaded.
variable "greeting" {
  type    = string
  default = "hello"
}

resource "terraform_data" "hello" {
  input = var.greeting
}

output "greeting" {
  value = terraform_data.hello.output
}
