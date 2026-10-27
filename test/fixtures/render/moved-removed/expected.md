<!-- tf-plan:moved-removed :: default -->
<details open>
<summary>📝 <b>Terraform Plan</b> — <code>moved-removed</code> (<code>default</code>) — 0 to add, 0 to change, 0 to destroy, 0 to replace, 0 to import, 2 to forget, 1 to move</summary>

**Exit code:** `2`  
**Job:** [#1234](https://ci.example/run/1234/job/5678)

| Add | Change | Destroy | Replace | **Import** | Forget | Move | Outputs |
|---|---|---|---|---|---|---|---|
| 0 | 0 | 0 | 0 | **0** | 2 | 1 | 0 |

<details open><summary>🚦 Diff summary</summary>

```diff
# terraform_data.pinned has moved to terraform_data.anchor
# (1 unchanged attribute hidden)
# terraform_data.item["alpha"] will no longer be managed by Terraform, but will not be destroyed
# (destroy = false is set in the configuration)
# (2 unchanged attributes hidden)
# terraform_data.item["beta"] will no longer be managed by Terraform, but will not be destroyed
# (destroy = false is set in the configuration)
# (2 unchanged attributes hidden)
```

</details>

<details open><summary>⚙️ Full terraform plan output</summary>

```terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:

Terraform will perform the following actions:

  # terraform_data.pinned has moved to terraform_data.anchor
    resource "terraform_data" "anchor" {
        id               = "e32ee727-5daf-3034-3f1d-8f7685469689"
        # (1 unchanged attribute hidden)
    }

 # terraform_data.item["alpha"] will no longer be managed by Terraform, but will not be destroyed
 # (destroy = false is set in the configuration)
 . resource "terraform_data" "item" {
        id     = "8f48d703-0260-81a6-15bf-ac48be42697c"
        # (2 unchanged attributes hidden)
    }

 # terraform_data.item["beta"] will no longer be managed by Terraform, but will not be destroyed
 # (destroy = false is set in the configuration)
 . resource "terraform_data" "item" {
        id     = "c4bd2c0a-1099-f921-e4eb-0252bb83585c"
        # (2 unchanged attributes hidden)
    }

Plan: 0 to add, 0 to change, 0 to destroy.
```

</details>

</details>
