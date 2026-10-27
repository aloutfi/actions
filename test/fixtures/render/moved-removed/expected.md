<!-- tf-plan:moved-removed :: default -->
<!-- tfpr:plan@1 {"key":"moved-removed:default","sha":"","head_sha":"","tree":"","exit":2,"status":"state_only","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:8773ec38e9b8ff68a5f9b9820671d5b1d51204f72c3e74ac6fea3a9d6b60bacd","run":"","attempt":0} -->
### 🔀 Terraform plan · `moved-removed` (`default`) · No infrastructure changes; state-only: 1 moved, 2 forgotten
> **2 resources will be forgotten**: removed from state, not destroyed; they keep running, unmanaged.
> Redaction: no redaction filter configured

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 | 0 | 1 | 2 | 0 | 0 |

**Resources (3)**
```diff
~   terraform_data.item["alpha"]  forget
~   terraform_data.item["beta"]   forget
>   terraform_data.anchor         move from terraform_data.pinned
```

<details><summary>Plan output for moved-removed</summary>

```terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:

Terraform will perform the following actions:

  # terraform_data.pinned has moved to terraform_data.anchor
    resource "terraform_data" "anchor" {
        id               = "e32ee727-5daf-3034-3f1d-8f7685469689"
    }

 # terraform_data.item["alpha"] will no longer be managed by Terraform, but will not be destroyed
 # (destroy = false is set in the configuration)
 . resource "terraform_data" "item" {
        id     = "8f48d703-0260-81a6-15bf-ac48be42697c"
    }

 # terraform_data.item["beta"] will no longer be managed by Terraform, but will not be destroyed
 # (destroy = false is set in the configuration)
 . resource "terraform_data" "item" {
        id     = "c4bd2c0a-1099-f921-e4eb-0252bb83585c"
    }

Plan: 0 to add, 0 to change, 0 to destroy.
```

</details>

[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · tfpr dev · redaction: none
