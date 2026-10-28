### 🔀 Terraform plan · `moves-only` (`default`) · No infrastructure changes; state-only: 3 moved
[plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 | 0 | 3 | 0 | 0 | 0 |

**Resources (3)**
```diff
>   terraform_data.anchor          move from terraform_data.pinned
>   terraform_data.entry["alpha"]  move from terraform_data.item["alpha"]
>   terraform_data.entry["beta"]   move from terraform_data.item["beta"]
```

<details open><summary>Plan output for moves-only</summary>

```terraform

Terraform will perform the following actions:

  # terraform_data.pinned has moved to terraform_data.anchor
    resource "terraform_data" "anchor" {
        id               = "6f6d936c-ec4e-10a5-6bc0-76a01ef26d92"
        # (1 unchanged attribute hidden)
    }

  # terraform_data.item["alpha"] has moved to terraform_data.entry["alpha"]
    resource "terraform_data" "entry" {
        id     = "dfdab144-94cc-ad7c-5419-ccde9ad06112"
        # (2 unchanged attributes hidden)
    }

  # terraform_data.item["beta"] has moved to terraform_data.entry["beta"]
    resource "terraform_data" "entry" {
        id     = "2ead3bbe-ab23-90b3-9aca-d678c9152ff0"
        # (2 unchanged attributes hidden)
    }

Plan: 0 to add, 0 to change, 0 to destroy.
```

</details>
