# test/fixtures/stack after a rename-only refactor: both resources move with
# moved blocks and nothing else changes. Planned against stack's state, this
# is a moves-only plan.

variable "items" {
  type    = map(string)
  default = { alpha = "one", beta = "two" }
}

resource "terraform_data" "entry" {
  for_each = var.items
  input    = each.value
}

resource "terraform_data" "anchor" {
  triggers_replace = "v1"
}

moved {
  from = terraform_data.item
  to   = terraform_data.entry
}

moved {
  from = terraform_data.pinned
  to   = terraform_data.anchor
}

output "label" {
  value = "tfpr"
}

output "item_count" {
  value = length(var.items)
}
