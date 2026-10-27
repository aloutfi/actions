# test/fixtures/stack plus an import block. terraform_data imports with an
# empty input, so the imported object is also updated: import plus update.

variable "items" {
  type    = map(string)
  default = { alpha = "one", beta = "two" }
}

resource "terraform_data" "item" {
  for_each = var.items
  input    = each.value
}

resource "terraform_data" "pinned" {
  triggers_replace = "v1"
}

resource "terraform_data" "adopted" {
  input = "adopted"
}

import {
  to = terraform_data.adopted
  id = "00000000-0000-4000-8000-000000000001"
}

output "label" {
  value = "tfpr"
}

output "item_count" {
  value = length(var.items)
}
