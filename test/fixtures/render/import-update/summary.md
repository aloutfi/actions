### 📝 Terraform plan · `import-update` (`default`) · 0 to add, 1 to change, 0 to destroy, 1 to import
[plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 1 | 0 | 0 | 1 | 0 | 0 | 0 | 0 |

**Resources (1)**
```diff
!   terraform_data.adopted  update, import
```

<details open><summary>Plan output for import-update</summary>

```terraform

Terraform used the selected providers to generate the following execution
plan. Resource actions are indicated with the following symbols:
  ~ update in-place

Terraform will perform the following actions:

  # terraform_data.adopted will be updated in-place
  # (imported from "00000000-0000-4000-8000-000000000001")
  ~ resource "terraform_data" "adopted" {
        id     = "00000000-0000-4000-8000-000000000001"
      + input  = "adopted"
      + output = (known after apply)
    }

Plan: 1 to import, 0 to add, 1 to change, 0 to destroy.
```

</details>
