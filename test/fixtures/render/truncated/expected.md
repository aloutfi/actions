<!-- tf-plan:truncated :: default -->
<details open>
<summary>📝 <b>Terraform Plan</b> — <code>truncated</code> (<code>default</code>) — 40 to add, 0 to change, 2 to destroy, 0 to replace, 0 to import</summary>

**Exit code:** `2`  
**Job:** [#1234](https://ci.example/run/1234/job/5678)

| Add | Change | Destroy | Replace | **Import** | Outputs |
|---|---|---|---|---|---|
| 40 | 0 | 2 | 0 | **0** | 1 |

<details open><summary>🚦 Diff summary</summary>

```diff
- terraform_data.item["alpha"] will be destroyed
# (because key ["alpha"] is not in for_each map)
- terraform_data.item["beta"] will be destroyed
# (because key ["beta"] is not in for_each map)
+ terraform_data.item["item01"] will be created
+ terraform_data.item["item02"] will be created
+ terraform_data.item["item03"] will be created
+ terraform_data.item["item04"] will be created
+ terraform_data.item["item05"] will be created
+ terraform_data.item["item06"] will be created
+ terraform_data.item["item07"] will be created
+ terraform_data.item["item08"] will be created
+ terraform_data.item["item09"] will be created
+ terraform_data.item["item10"] will be created
+ terraform_data.item["item11"] will be created
+ terraform_data.item["item12"] will be created
+ terraform_data.item["item13"] will be created
+ terraform_data.item["item14"] will be created
+ terraform_data.item["item15"] will be created
+ terraform_data.item["item16"] will be created
+ terraform_data.item["item17"] will be created
+ terraform_data.item["item18"] will be created
+ terraform_data.item["item19"] will be created
+ terraform_data.item["item20"] will be created
+ terraform_data.item["item21"] will be created
+ terraform_data.item["item22"] will be created
+ terraform_data.item["item23"] will be created
+ terraform_data.item["item24"] will be created
+ terraform_data.item["item25"] will be created
+ terraform_data.item["item26"] will be created
+ terraform_data.item["item27"] will be created
+ terraform_data.item["item28"] will be created
+ terraform_data.item["item29"] will be created
+ terraform_data.item["item30"] will be created
+ terraform_data.item["item31"] will be created
+ terraform_data.item["item32"] will be created
+ terraform_data.item["item33"] will be created
+ terraform_data.item["item34"] will be created
+ terraform_data.item["item35"] will be created
+ terraform_data.item["item36"] will be created
+ terraform_data.item["item37"] will be created
+ terraform_data.item["item38"] will be created
+ terraform_data.item["item39"] will be created
+ terraform_data.item["item40"] will be created
```

</details>

<details open><summary>⚙️ Full terraform plan output</summary>

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
  + r

[truncated — download the tfplan artifact and run `terraform show tfplan` for full output]
```

</details>

</details>
