#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077

# Run from the repository root. A new output directory prevents stale assets.
version=${1:-dev}
out=${2:-dist}
case "$version" in
  ''|*[!a-zA-Z0-9.-]*) echo 'invalid version' >&2; exit 1 ;;
esac
revision=$(git rev-parse HEAD)
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
  revision="$revision-dirty"
fi
if [ "$version" != dev ]; then
  case "$version" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo 'expected a version tag' >&2; exit 1 ;; esac
  [ "$revision" = "$(git rev-parse "$version^{commit}")" ] || {
    echo 'release requires a clean checkout at the requested tag' >&2; exit 1;
  }
fi
mkdir "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir "$work/licenses"
cp "$(go env GOROOT)/LICENSE" "$work/licenses/Go-LICENSE"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  env CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags "-X github.com/llm-measurement/fleetdiff/internal/cli.Version=$version -X github.com/llm-measurement/fleetdiff/internal/cli.Revision=$revision" \
    -o "$work/fleetdiff" ./cmd/fleetdiff
  cp LICENSE "$work/LICENSE"
  env CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go list -mod=readonly -deps \
    -f '{{with .Module}}{{.Path}} {{.Dir}}{{end}}' ./cmd/fleetdiff > "$work/modules.txt"
  sort -u "$work/modules.txt" > "$work/modules-sorted.txt"
  while read -r module directory; do
    [ -n "$module" ] || continue
    [ "$module" != github.com/llm-measurement/fleetdiff ] || continue
    destination="$work/licenses/$module"
    mkdir -p "$destination"
    found=false
    for name in LICENSE LICENSE.md LICENSE.txt COPYING; do
      if [ -f "$directory/$name" ]; then
        [ -f "$destination/$name" ] || cp "$directory/$name" "$destination/"
        found=true
      fi
    done
    [ "$found" = true ] || { echo "dependency license missing: $module" >&2; exit 1; }
    for name in NOTICE NOTICE.md NOTICE.txt; do
      if [ -f "$directory/$name" ] && [ ! -f "$destination/$name" ]; then
        cp "$directory/$name" "$destination/"
      fi
    done
  done < "$work/modules-sorted.txt"
  chmod 755 "$work/fleetdiff"
  chmod 644 "$work/LICENSE"
  chmod -R u=rwX,go=rX "$work/licenses"
  archive="$out/fleetdiff_${version}_${os}_${arch}.tar.gz"
  # Do not embed the builder's local user/group names or filesystem attributes.
  case "$(uname -s)" in
    Darwin) COPYFILE_DISABLE=1 tar --uid=0 --gid=0 --uname=root --gname=root \
      --no-xattrs --no-acls --no-fflags -czf "$archive" -C "$work" fleetdiff LICENSE licenses ;;
    Linux) tar --owner=0 --group=0 --numeric-owner --no-xattrs --no-acls \
      -czf "$archive" -C "$work" fleetdiff LICENSE licenses ;;
    *) echo 'release packaging requires Linux or macOS' >&2; exit 1 ;;
  esac
done
(cd "$out" && shasum -a 256 ./*.tar.gz > SHA256SUMS)
printf 'version=%s\nrevision=%s\ntoolchain=%s\n' "$version" "$revision" "$(go version)" > "$out/build.txt"
