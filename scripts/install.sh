#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077

usage() {
  printf '%s\n' 'Usage: sh scripts/install.sh [VERSION [NEW_DIRECTORY]]' \
    'Defaults: v0.3.1 ./fleetdiff-install. Requires curl, gh, shasum, and tar.'
}
case "${1:-}" in --help|-h) usage; exit 0 ;; esac
if [ "$#" -gt 2 ]; then usage >&2; exit 2; fi
version=${1-v0.3.1}
destination=${2-./fleetdiff-install}
case "$version" in
  ''|*[!a-zA-Z0-9.-]*) printf '%s\n' 'Supply a release version such as v0.3.0.' >&2; exit 2 ;;
esac
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) printf '%s\n' 'Supply a release version such as v0.3.0.' >&2; exit 2 ;;
esac
case "$destination" in
  '') printf '%s\n' 'Supply a new installation directory.' >&2; exit 2 ;;
  /*) ;;
  *) destination="./$destination" ;;
esac

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) printf '%s\n' 'Supported operating systems: Linux and macOS.' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) printf '%s\n' 'Supported architectures: AMD64 and ARM64.' >&2; exit 1 ;;
esac
for command in curl gh shasum tar; do
  if ! command -v "$command" >/dev/null 2>&1; then
    printf 'Install %s before running this installer.\n' "$command" >&2
    exit 1
  fi
done

# Keep downloads and verification evidence together; never reuse a prior install.
mkdir "$destination"
cd "$destination"
archive="fleetdiff_${version}_${os}_${arch}.tar.gz"
base="https://github.com/llm-measurement/fleetdiff/releases/download/$version"
for asset in "$archive" SHA256SUMS provenance.jsonl; do
  curl --fail --location --proto '=https' --tlsv1.2 --remote-name "$base/$asset"
done
for asset in "$archive" SHA256SUMS; do
  gh attestation verify "$asset" --bundle provenance.jsonl \
    --repo llm-measurement/fleetdiff \
    --signer-workflow llm-measurement/fleetdiff/.github/workflows/release.yml \
    --source-ref "refs/tags/$version" --deny-self-hosted-runners
done
test -s "$archive"
shasum -a 256 --ignore-missing -c SHA256SUMS
tar -xzf "$archive"
printf 'Verified %s/%s installation in %s\n' "$os" "$arch" "$destination"
printf '%s\n' 'Run fleetdiff --version from that directory before use.'
