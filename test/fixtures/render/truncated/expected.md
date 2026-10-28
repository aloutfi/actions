<!-- tf-plan:truncated :: default -->
<!-- tfpr:plan@1 {"key":"truncated:default","sha":"","head_sha":"","tree":"","exit":2,"status":"changes","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:458f12ab4b9a187645281d29abf2db46c3396fdbcbf8e05e1af8ec127ada5756","run":"","attempt":0} -->
### ⚠️ Terraform plan · `truncated` (`default`) · 40 to add, 0 to change, 2 to destroy
[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · [plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · redaction: none

> **Plan text truncated**: showing 2,933 of 8,766 characters; the full plan is linked above.
> **Resource list shortened**: every delete, replace and forget is listed; 2 lower-risk lines are counted, not listed.

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
```
+ 2 more `create` lines

<details><summary>Plan output for truncated (truncated: 2,933 of 8,766 characters)</summary>

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
```

</details>

Outputs: `item_count` (update)
