# test/fixtures/stack with terraform_data.pinned dropped from state by a
# removed block (destroy = false). Planned against stack's state, the only
# change is a forget: the object keeps existing, unmanaged.

variable "items" {
  type    = map(string)
  default = { alpha = "one", beta = "two" }
}

resource "terraform_data" "item" {
  for_each = var.items
  input    = each.value
}

removed {
  from = terraform_data.pinned

  lifecycle {
    destroy = false
  }
}

output "label" {
  value = "tfpr"
}

output "item_count" {
  value = length(var.items)
}
