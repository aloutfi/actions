<!-- tf-plan:forget :: default -->
<!-- tfpr:plan@1 {"key":"forget:default","sha":"","head_sha":"","tree":"","exit":2,"status":"state_only","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:75cf4e944a9d5524cf19dc00dcda2cdeaae93924a247d9c08ef4ef1546ad5363","run":"","attempt":0} -->
### 🔀 Terraform plan · `forget` (`default`) · No infrastructure changes; state-only: 1 forgotten
[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · [plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · redaction: none

> **1 resource will be forgotten**: removed from state, not destroyed; it keeps running, unmanaged.

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 | 0 |

**Resources (1)**
```diff
~   terraform_data.pinned  forget
```

<details><summary>Plan output for forget</summary>

```terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:

Terraform will perform the following actions:

 # terraform_data.pinned will no longer be managed by Terraform, but will not be destroyed
 # (destroy = false is set in the configuration)
 . resource "terraform_data" "pinned" {
        id               = "6f6d936c-ec4e-10a5-6bc0-76a01ef26d92"
    }

Plan: 0 to add, 0 to change, 0 to destroy.
```

</details>
