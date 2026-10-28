<!-- tf-plan:moves-only :: default -->
<!-- tfpr:plan@1 {"key":"moves-only:default","sha":"","head_sha":"","tree":"","exit":2,"status":"state_only","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:52393767f3a4aa688e6287420189a33bfbbf57e616970c9bd0539737c6b4bd94","run":"","attempt":0} -->
### 🔀 Terraform plan · `moves-only` (`default`) · No infrastructure changes; state-only: 3 moved
[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · [plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 | 0 | 3 | 0 | 0 | 0 |

**Resources (3)**
```diff
>   terraform_data.anchor          move from terraform_data.pinned
>   terraform_data.entry["alpha"]  move from terraform_data.item["alpha"]
>   terraform_data.entry["beta"]   move from terraform_data.item["beta"]
```

<details><summary>Plan output for moves-only</summary>

```terraform

Terraform will perform the following actions:

  # terraform_data.pinned has moved to terraform_data.anchor
    resource "terraform_data" "anchor" {
        id               = "6f6d936c-ec4e-10a5-6bc0-76a01ef26d92"
    }

  # terraform_data.item["alpha"] has moved to terraform_data.entry["alpha"]
    resource "terraform_data" "entry" {
        id     = "dfdab144-94cc-ad7c-5419-ccde9ad06112"
    }

  # terraform_data.item["beta"] has moved to terraform_data.entry["beta"]
    resource "terraform_data" "entry" {
        id     = "2ead3bbe-ab23-90b3-9aca-d678c9152ff0"
    }

Plan: 0 to add, 0 to change, 0 to destroy.
```

</details>
