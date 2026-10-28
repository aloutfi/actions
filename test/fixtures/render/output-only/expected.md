<!-- tf-plan:output-only :: default -->
<!-- tfpr:plan@1 {"key":"output-only:default","sha":"","head_sha":"","tree":"","exit":2,"status":"outputs_only","units":1,"apply":["."],"provisional":[],"planset":"","fp":"sha256:f91a7080f302551ad6906f1ab7d77adc32b06acb8e83ae322f2d40375678479f","run":"","attempt":0} -->
### 🔸 Terraform plan · `output-only` (`default`) · No resource changes; 1 output changes. Downstream `terraform_remote_state` readers are affected
[job log](https://ci.example/run/1234/job/5678) · [full plan](https://ci.example/run/1234#summary) · [plan.txt](https://ci.example/run/1234/artifacts/plan.txt) · tfpr dev · redaction: none

| Add | Change | Destroy | Replace | Import | Move | Forget | Outputs | Drift |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 0 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |

<details><summary>Plan output for output-only</summary>

```terraform

Changes to Outputs:
  ~ label      = "tfpr" -> "renamed"

You can apply this plan to save these new output values to the Terraform
state, without changing any real infrastructure.
```

</details>

Outputs: `label` (update)
