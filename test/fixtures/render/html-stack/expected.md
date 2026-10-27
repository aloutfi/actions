<!-- tf-plan:envs/<prod>&"x" :: default -->
<!-- tfpr:plan@1 {"key":"envs-prodx:default","sha":"","head_sha":"","tree":"","exit":2,"status":"changes","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:6715d3894517c87739a2967441c2644874087a21eab0c4d5f4f63bf792ce1f61","run":"","attempt":0} -->
### ⚠️ Terraform plan · `envs/<prod>&"x"` (`default`) · 1 to add, 1 to change, 1 to destroy, 1 to replace

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1 | 1 | 1 | 0 | 0 | 0 | 0 | 0 |

**Resources (4)**
```diff
-   terraform_data.item["beta"]   delete
-/+ terraform_data.pinned         replace
!   terraform_data.item["alpha"]  update
+   terraform_data.item["gamma"]  create
```

<details><summary>Plan output for envs/&lt;prod&gt;&amp;&#34;x&#34;</summary>

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
        id     = "dfdab144-94cc-ad7c-5419-ccde9ad06112"
      ~ input  = "one" -> "uno"
      ~ output = "one" -> (known after apply)
    }

  # terraform_data.item["beta"] will be destroyed
  # (because key ["beta"] is not in for_each map)
  - resource "terraform_data" "item" {
      - id     = "2ead3bbe-ab23-90b3-9aca-d678c9152ff0" -> null
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
      ~ id               = "6f6d936c-ec4e-10a5-6bc0-76a01ef26d92" -> (known after apply)
      ~ triggers_replace = "v1" -> "v2"
    }

Plan: 2 to add, 1 to change, 2 to destroy.
```

</details>

[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · tfpr dev · redaction: none
