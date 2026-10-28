### ⚠️ Terraform plan · `truncated` (`default`) · 40 to add, 0 to change, 2 to destroy
[plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · [terraform 1.15.4](https://github.com/hashicorp/terraform/releases/tag/v1.15.4) · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 40 | 0 | 2 | 0 | 0 | 0 | 0 | 1 | 0 |

**Resources (42)**
```diff
-   terraform_data.item["alpha"]   delete
-   terraform_data.item["beta"]    delete
+   terraform_data.item["item01"]  create
+   terraform_data.item["item02"]  create
+   terraform_data.item["item03"]  create
+   terraform_data.item["item04"]  create
+   terraform_data.item["item05"]  create
+   terraform_data.item["item06"]  create
+   terraform_data.item["item07"]  create
+   terraform_data.item["item08"]  create
+   terraform_data.item["item09"]  create
+   terraform_data.item["item10"]  create
+   terraform_data.item["item11"]  create
+   terraform_data.item["item12"]  create
+   terraform_data.item["item13"]  create
+   terraform_data.item["item14"]  create
+   terraform_data.item["item15"]  create
+   terraform_data.item["item16"]  create
+   terraform_data.item["item17"]  create
+   terraform_data.item["item18"]  create
+   terraform_data.item["item19"]  create
+   terraform_data.item["item20"]  create
+   terraform_data.item["item21"]  create
+   terraform_data.item["item22"]  create
+   terraform_data.item["item23"]  create
+   terraform_data.item["item24"]  create
+   terraform_data.item["item25"]  create
+   terraform_data.item["item26"]  create
+   terraform_data.item["item27"]  create
+   terraform_data.item["item28"]  create
+   terraform_data.item["item29"]  create
+   terraform_data.item["item30"]  create
+   terraform_data.item["item31"]  create
+   terraform_data.item["item32"]  create
+   terraform_data.item["item33"]  create
+   terraform_data.item["item34"]  create
+   terraform_data.item["item35"]  create
+   terraform_data.item["item36"]  create
+   terraform_data.item["item37"]  create
+   terraform_data.item["item38"]  create
+   terraform_data.item["item39"]  create
+   terraform_data.item["item40"]  create
```

<details open><summary>Plan output for truncated</summary>

```terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  + create
  - destroy

Terraform will perform the following actions:

  # terraform_data.item["alpha"] will be destroyed
  # (because key ["alpha"] is not in for_each map)
  - resource "terraform_data" "item" {
      - id     = "efc3b390-f174-9156-bb86-a1a7cef5dc52" -> null
      - input  = "one" -> null
      - output = "one" -> null
    }

  # terraform_data.item["beta"] will be destroyed
  # (because key ["beta"] is not in for_each map)
  - resource "terraform_data" "item" {
      - id     = "67316145-e488-8477-2a78-e6bb5aebb391" -> null
      - input  = "two" -> null
      - output = "two" -> null
    }

  # terraform_data.item["item01"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-01"
      + output = (known after apply)
    }

  # terraform_data.item["item02"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-02"
      + output = (known after apply)
    }

  # terraform_data.item["item03"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-03"
      + output = (known after apply)
    }

  # terraform_data.item["item04"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-04"
      + output = (known after apply)
    }

  # terraform_data.item["item05"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-05"
      + output = (known after apply)
    }

  # terraform_data.item["item06"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-06"
      + output = (known after apply)
    }

  # terraform_data.item["item07"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-07"
      + output = (known after apply)
    }

  # terraform_data.item["item08"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-08"
      + output = (known after apply)
    }

  # terraform_data.item["item09"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-09"
      + output = (known after apply)
    }

  # terraform_data.item["item10"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-10"
      + output = (known after apply)
    }

  # terraform_data.item["item11"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-11"
      + output = (known after apply)
    }

  # terraform_data.item["item12"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-12"
      + output = (known after apply)
    }

  # terraform_data.item["item13"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-13"
      + output = (known after apply)
    }

  # terraform_data.item["item14"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-14"
      + output = (known after apply)
    }

  # terraform_data.item["item15"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-15"
      + output = (known after apply)
    }

  # terraform_data.item["item16"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-16"
      + output = (known after apply)
    }

  # terraform_data.item["item17"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-17"
      + output = (known after apply)
    }

  # terraform_data.item["item18"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-18"
      + output = (known after apply)
    }

  # terraform_data.item["item19"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-19"
      + output = (known after apply)
    }

  # terraform_data.item["item20"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-20"
      + output = (known after apply)
    }

  # terraform_data.item["item21"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-21"
      + output = (known after apply)
    }

  # terraform_data.item["item22"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-22"
      + output = (known after apply)
    }

  # terraform_data.item["item23"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-23"
      + output = (known after apply)
    }

  # terraform_data.item["item24"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-24"
      + output = (known after apply)
    }

  # terraform_data.item["item25"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-25"
      + output = (known after apply)
    }

  # terraform_data.item["item26"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-26"
      + output = (known after apply)
    }

  # terraform_data.item["item27"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-27"
      + output = (known after apply)
    }

  # terraform_data.item["item28"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-28"
      + output = (known after apply)
    }

  # terraform_data.item["item29"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-29"
      + output = (known after apply)
    }

  # terraform_data.item["item30"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-30"
      + output = (known after apply)
    }

  # terraform_data.item["item31"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-31"
      + output = (known after apply)
    }

  # terraform_data.item["item32"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-32"
      + output = (known after apply)
    }

  # terraform_data.item["item33"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-33"
      + output = (known after apply)
    }

  # terraform_data.item["item34"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-34"
      + output = (known after apply)
    }

  # terraform_data.item["item35"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-35"
      + output = (known after apply)
    }

  # terraform_data.item["item36"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-36"
      + output = (known after apply)
    }

  # terraform_data.item["item37"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-37"
      + output = (known after apply)
    }

  # terraform_data.item["item38"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-38"
      + output = (known after apply)
    }

  # terraform_data.item["item39"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-39"
      + output = (known after apply)
    }

  # terraform_data.item["item40"] will be created
  + resource "terraform_data" "item" {
      + id     = (known after apply)
      + input  = "value-40"
      + output = (known after apply)
    }

Plan: 40 to add, 0 to change, 2 to destroy.

Changes to Outputs:
  ~ item_count = 2 -> 40
```

</details>

Outputs: `item_count` (update)
