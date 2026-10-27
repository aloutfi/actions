#!/usr/bin/env bash
# Regenerate test/fixtures/render/<case>/{plan.json,plan.txt,exit_code} with
# real terraform (local backend, terraform_data only: no credentials). Run
# test/run.sh --update afterwards to refresh the expected comment bodies.
#
# Usage: test/fixtures/generate.sh [case...]   (default: every case)
#
# Regenerating a case changes its terraform_data ids, so name only the cases
# you mean to change.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="${here}/render"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

all=(no-changes changes output-only truncated moved-removed moves-only forget import-update backticks html-stack large)
wanted=("$@")
[[ $# -gt 0 ]] || wanted=("${all[@]}")
want() {
  local c
  for c in "${wanted[@]}"; do [[ "${c}" == "$1" ]] && return 0; done
  return 1
}

# stack DIR CONFIG: a working copy of CONFIG (a fixture stack) in DIR,
# initialized, with the baseline state applied when no state was copied in.
stack() {
  mkdir -p "$1"
  cp "${here}/$2/"*.tf "$1/"
  (cd "$1" && terraform init -input=false > /dev/null)
  if [[ ! -f "$1/terraform.tfstate" ]]; then
    (cd "$1" && terraform apply -input=false -auto-approve > /dev/null)
  fi
}

# variant NAME CONFIG: CONFIG planned against the baseline stack's state.
variant() {
  mkdir -p "${work}/$1"
  cp "${work}/base/terraform.tfstate" "${work}/$1/"
  stack "${work}/$1" "$2"
}

# capture CASE DIR [terraform plan args...]: plan in DIR and write the case.
capture() {
  local name="$1" dir="$2" code=0
  shift 2
  (cd "${dir}" && terraform plan -input=false -no-color -detailed-exitcode -out=tfplan "$@" > /dev/null) || code=$?
  [[ "${code}" -ne 1 ]] || exit 1
  mkdir -p "${out}/${name}"
  (cd "${dir}" && terraform show -json tfplan) | jq . > "${out}/${name}/plan.json"
  (cd "${dir}" && terraform show -no-color tfplan) > "${out}/${name}/plan.txt"
  echo "${code}" > "${out}/${name}/exit_code"
}

stack "${work}/base" stack
base="${work}/base"

want no-changes && capture no-changes "${base}"
want changes && capture changes "${base}" -var 'items={alpha="uno",gamma="three"}' -var 'replace_trigger=v2'
want output-only && capture output-only "${base}" -var 'label=renamed'
if want truncated; then
  capture truncated "${base}" -var "items={$(for i in $(seq -w 1 40); do printf 'item%s="value-%s",' "$i" "$i"; done)}"
  # A small budget keeps the golden file readable.
  echo 6000 > "${out}/truncated/budget"
fi
# A for_each key and a value with backtick runs: neither may break a fence.
# shellcheck disable=SC2016 # literal backticks
want backticks && capture backticks "${base}" -var 'items={"a```b"="x````y",alpha="one",beta="two"}'
if want html-stack; then
  # The stack name lands in HTML (<summary>) and in markdown code spans.
  capture html-stack "${base}" -var 'items={alpha="uno",gamma="three"}' -var 'replace_trigger=v2'
  printf '%s\n' 'envs/<prod>&"x"' > "${out}/html-stack/stack"
fi
if want moved-removed; then
  # moved {} and removed {} (destroy = false).
  variant refactor stack-refactor
  capture moved-removed "${work}/refactor"
fi
if want moves-only; then
  variant moves stack-moves
  capture moves-only "${work}/moves"
fi
if want forget; then
  variant forget stack-forget
  capture forget "${work}/forget"
fi
if want import-update; then
  variant import stack-import
  capture import-update "${work}/import"
fi
if want large; then
  # 3,000 creates, 25 destroys, 5 replaces, 40 updates. The plan is stored
  # gzipped (compact JSON); test/run.sh and the Go tests unzip it.
  stack "${work}/large" stack-large
  capture large "${work}/large" -var bulk=3000 -var legacy=0 -var generation=v2
  jq -c . "${out}/large/plan.json" | gzip -9n > "${out}/large/plan.json.gz"
  gzip -9n < "${out}/large/plan.txt" > "${out}/large/plan.txt.gz"
  rm "${out}/large/plan.json" "${out}/large/plan.txt"
fi
