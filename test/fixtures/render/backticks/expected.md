<!-- tf-plan:backticks :: default -->
<!-- tfpr:plan@1 {"key":"backticks:default","sha":"","head_sha":"","tree":"","exit":2,"status":"changes","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:a417d4866354d7a03a8ce6853617b25ccb3bb68cbca6aeb462dcf2c2034ec588","run":"","attempt":0} -->
### 📝 Terraform plan · `backticks` (`default`) · 1 to add, 0 to change, 0 to destroy
[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · [plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · [terraform 1.15.4](https://github.com/hashicorp/terraform/releases/tag/v1.15.4) · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |

**Resources (1)**
````diff
+   terraform_data.item["a```b"]  create
````

<details><summary>Plan output for backticks</summary>

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
