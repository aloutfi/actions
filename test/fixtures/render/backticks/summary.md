### 📝 Terraform plan · `backticks` (`default`) · 1 to add, 0 to change, 0 to destroy
[plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · [terraform 1.15.4](https://github.com/hashicorp/terraform/releases/tag/v1.15.4) · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |

**Resources (1)**
````diff
+   terraform_data.item["a```b"]  create
````

<details open><summary>Plan output for backticks</summary>

`````terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  + create

Terraform will perform the following actions:

  # terraform_data.item["a```b"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "x````y"
      + output = (known after apply)
    }

Plan: 1 to add, 0 to change, 0 to destroy.

Changes to Outputs:
  ~ item_count = 2 -> 3
`````

</details>

Outputs: `item_count` (update)
