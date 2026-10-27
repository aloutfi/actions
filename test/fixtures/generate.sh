#!/usr/bin/env bash
# Regenerate test/fixtures/render/*/{plan.json,plan.txt,exit_code} with real
# terraform against test/fixtures/stack (and stack-refactor for moved-removed). Run test/run.sh --update afterwards
# to refresh the expected comment bodies.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="${here}/render"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
cp "${here}/stack/"*.tf "${work}/"
cd "${work}"

terraform init -input=false > /dev/null
terraform apply -input=false -auto-approve > /dev/null

# capture CASE [terraform plan args...]
capture() {
  local name="$1" code=0
  shift
  terraform plan -input=false -no-color -detailed-exitcode -out=tfplan "$@" > /dev/null || code=$?
  [[ "${code}" -ne 1 ]] || exit 1
  mkdir -p "${out}/${name}"
  terraform show -json tfplan | jq . > "${out}/${name}/plan.json"
  terraform show -no-color tfplan > "${out}/${name}/plan.txt"
  echo "${code}" > "${out}/${name}/exit_code"
}

capture no-changes
capture changes -var 'items={alpha="uno",gamma="three"}' -var 'replace_trigger=v2'
capture output-only -var 'label=renamed'
capture truncated -var "items={$(for i in $(seq -w 1 40); do printf 'item%s="value-%s",' "$i" "$i"; done)}"
# moved {} and removed {} (destroy = false) against the same baseline state.
cp "${here}/stack-refactor/main.tf" main.tf
capture moved-removed

# The truncated case renders with a small budget so the golden file stays readable.
echo 2000 > "${out}/truncated/budget"
