# A large stack for the comment budget: thousands of creates plus a few
# destroys, replaces and updates. The baseline (defaults) has no bulk
# resources; generate.sh plans bulk = 3000, legacy = 0 and new generations.
# The dogfood job plans it with no state, which is all creates.

variable "bulk" {
  type    = number
  default = 0
}

variable "legacy" {
  type    = number
  default = 25
}

variable "generation" {
  type    = string
  default = "v1"
}

resource "terraform_data" "bulk" {
  count = var.bulk
  input = "bulk-${count.index}"
}

resource "terraform_data" "legacy" {
  count = var.legacy
  input = "legacy-${count.index}"
}

resource "terraform_data" "tuned" {
  count = 40
  input = "${var.generation}-${count.index}"
}

resource "terraform_data" "pinned" {
  count            = 5
  triggers_replace = var.generation
}

output "bulk_count" {
  value = var.bulk
}
