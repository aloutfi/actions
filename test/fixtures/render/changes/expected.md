<!-- tf-plan:changes :: default -->
<details open>
<summary>📝 <b>Terraform Plan</b> — <code>changes</code> (<code>default</code>) — 1 to add, 1 to change, 1 to destroy, 1 to replace, 0 to import</summary>

**Exit code:** `2`  
**Job:** [#1234](https://ci.example/run/1234/job/5678)

| Add | Change | Destroy | Replace | **Import** | Forget | Move | Outputs |
|---|---|---|---|---|---|---|---|
| 1 | 1 | 1 | 1 | **0** | 0 | 0 | 0 |

<details open><summary>🚦 Diff summary</summary>

```diff
! terraform_data.item["alpha"] will be updated in-place
- terraform_data.item["beta"] will be destroyed
# (because key ["beta"] is not in for_each map)
+ terraform_data.item["gamma"] will be created
! terraform_data.pinned must be replaced
```

</details>

<details open><summary>⚙️ Full terraform plan output</summary>

```terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  + create
  ~ update in-place
  - destroy
-/+ destroy and then create replacement

Terraform will perform the following actions:

  # terraform_data.item["alpha"] will be updated in-place
  ~ resource "terraform_data" "item" {
        id     = "efc3b390-f174-9156-bb86-a1a7cef5dc52"
      ~ input  = "one" -> "uno"
      ~ output = "one" -> (known after apply)
    }

  # terraform_data.item["beta"] will be destroyed
  # (because key ["beta"] is not in for_each map)
  - resource "terraform_data" "item" {
      - id     = "67316145-e488-8477-2a78-e6bb5aebb391" -> null
      - input  = "two" -> null
      - output = "two" -> null
    }

  # terraform_data.item["gamma"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "three"
      + output = (known after apply)
    }

  # terraform_data.pinned must be replaced
-/+ resource "terraform_data" "pinned" {
      ~ id               = "5cd9da4f-7943-38cf-689f-4c639089e9ec" -> (known after apply)
      ~ triggers_replace = "v1" -> "v2"
    }

Plan: 2 to add, 1 to change, 2 to destroy.
```

</details>

</details>
