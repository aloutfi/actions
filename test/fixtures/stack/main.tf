# Tiny local-backend stack used to generate render fixtures and for the
# dogfood plan in CI. terraform_data needs no provider credentials.

variable "items" {
  type    = map(string)
  default = { alpha = "one", beta = "two" }
}

variable "replace_trigger" {
  type    = string
  default = "v1"
}

variable "label" {
  type    = string
  default = "tfpr"
}

resource "terraform_data" "item" {
  for_each = var.items
  input    = each.value
}

resource "terraform_data" "pinned" {
  triggers_replace = var.replace_trigger
}

output "label" {
  value = var.label
}

output "item_count" {
  value = length(var.items)
}
