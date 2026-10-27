### 🔸 Terraform plan · `output-only` (`default`) · No resource changes; 1 output changes. Downstream `terraform_remote_state` readers are affected

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |

<details open><summary>Plan output for output-only</summary>

```terraform

Changes to Outputs:
  ~ label      = "tfpr" -> "renamed"

You can apply this plan to save these new output values to the Terraform
state, without changing any real infrastructure.
```

</details>

Outputs: `label` (update)

tfpr dev · redaction: none
