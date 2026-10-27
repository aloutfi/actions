#!/usr/bin/env bash
# Puts the tfpr binary on disk for the composite actions and writes step
# outputs to $GITHUB_OUTPUT:
#   path=<binary>  the binary is ready
#   build=true     this ref has no release; the action sets up Go and runs
#                  `install-tfpr.sh build`, which writes path=<binary>
#
# Usage: install-tfpr.sh [locate|build]
#   locate (default), in order:
#     1. $TFPR_BIN, a prebuilt binary (this repo's own CI, local testing).
#     2. The release archive named in checksums.txt, committed in the same tree
#        as the action. It is downloaded from this repo's GitHub release and
#        must match the committed sha256, so pinning the action's ref pins the
#        binary's bytes.
#     3. Otherwise build=true: unreleased refs build from source.
#   build: go build ./cmd/tfpr from the action's own checkout.
set -euo pipefail

repo="aloutfi/actions"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="${GITHUB_OUTPUT:-/dev/stdout}"

die() {
  echo "::error::install-tfpr: $*"
  exit 1
}

case "${RUNNER_OS:-$(uname -s)}" in
  Linux) os=linux ;;
  macOS | Darwin) os=darwin ;;
  Windows) os=windows ;;
  *) die "unsupported runner OS '${RUNNER_OS:-}'" ;;
esac
case "${RUNNER_ARCH:-$(uname -m)}" in
  X64 | x86_64) arch=amd64 ;;
  ARM64 | arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported runner architecture '${RUNNER_ARCH:-}'" ;;
esac
exe=tfpr
[[ "${os}" != windows ]] || exe=tfpr.exe
dest="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/tfpr.XXXXXX")"

sha256_check() {
  if command -v sha256sum > /dev/null; then sha256sum -c -; else shasum -a 256 -c -; fi
}

case "${1:-locate}" in
  locate)
    if [[ -n "${TFPR_BIN:-}" ]]; then
      [[ -x "${TFPR_BIN}" ]] || die "TFPR_BIN '${TFPR_BIN}' is not an executable file"
      echo "Using TFPR_BIN=${TFPR_BIN}"
      echo "path=${TFPR_BIN}" >> "${out}"
      exit 0
    fi
    entry=""
    if [[ -f "${root}/checksums.txt" ]]; then
      entry="$(grep -E "^[0-9a-f]{64}  tfpr_[^ ]+_${os}_${arch}\.tar\.gz$" "${root}/checksums.txt" | head -n 1 || true)"
    fi
    if [[ -z "${entry}" ]]; then
      echo "No released tfpr for ${os}/${arch} at this ref; building from source."
      echo "build=true" >> "${out}"
      exit 0
    fi
    file="${entry##* }"
    version="${file#tfpr_}"
    version="${version%_"${os}"_"${arch}".tar.gz}"
    url="https://github.com/${repo}/releases/download/v${version}/${file}"
    echo "Downloading ${url}"
    curl -fsSL --retry 3 -o "${dest}/${file}" "${url}"
    (cd "${dest}" && echo "${entry}" | sha256_check) || die "${file} does not match checksums.txt"
    tar -xzf "${dest}/${file}" -C "${dest}" "${exe}"
    ;;
  build)
    (cd "${root}" && CGO_ENABLED=0 go build -trimpath -o "${dest}/${exe}" ./cmd/tfpr)
    ;;
  *) die "usage: install-tfpr.sh [locate|build]" ;;
esac
echo "path=${dest}/${exe}" >> "${out}"
