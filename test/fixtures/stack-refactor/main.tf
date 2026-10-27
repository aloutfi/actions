# test/fixtures/stack after a refactor: terraform_data.pinned is renamed with
# a moved block, and terraform_data.item is dropped from state with a removed
# block (destroy = false). Planned against stack's state, this is an exit-2
# plan with no creates, updates, destroys or replaces.

resource "terraform_data" "anchor" {
  triggers_replace = "v1"
}

moved {
  from = terraform_data.pinned
  to   = terraform_data.anchor
}

removed {
  from = terraform_data.item

  lifecycle {
    destroy = false
  }
}

output "label" {
  value = "tfpr"
}

output "item_count" {
  value = 2
}
