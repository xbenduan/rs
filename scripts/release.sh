#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' "$ROOT/server/server.go")"
RELEASES="$ROOT/releases"

if [[ -z "$VERSION" ]]; then
  echo "error: cannot read version from server/server.go" >&2
  exit 1
fi
escaped_version="${VERSION//./\\.}"
if ! grep -qE "^## v${escaped_version}([[:space:]]|$)" "$ROOT/CHANGELOG.md"; then
  echo "error: CHANGELOG.md has no v$VERSION entry" >&2
  exit 1
fi

go_version="$(go env GOVERSION)"
if ! awk -v actual="${go_version#go}" 'BEGIN {
  split(actual, a, ".")
  exit !((a[1] > 1) || (a[1] == 1 && a[2] > 26) || (a[1] == 1 && a[2] == 26 && a[3] >= 9))
}'; then
  echo "error: Go 1.26.9 or newer is required, found $go_version" >&2
  exit 1
fi

(
  cd "$ROOT"
  unformatted="$(gofmt -l $(go list -f '{{.Dir}}' ./...))"
  if [[ -n "$unformatted" ]]; then
    echo "error: gofmt needed:" >&2
    echo "$unformatted" >&2
    exit 1
  fi
  go vet ./...
  GOOS=windows go vet ./...
  go test -race -count=1 ./...
)

rm -rf "$RELEASES"
mkdir -p "$RELEASES"

for target in \
  darwin/amd64 \
  darwin/arm64 \
  linux/amd64 \
  linux/arm64 \
  windows/amd64 \
  windows/arm64
do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  suffix=""
  [[ "$GOOS" == "windows" ]] && suffix=".exe"
  output="$RELEASES/rs-$GOOS-$GOARCH$suffix"

  echo "building ${GOOS}/${GOARCH}"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -buildvcs=false -trimpath -ldflags="-s -w" -o "$output" .
  )
done

(
  cd "$ROOT/skill"
  zip -qrX "$RELEASES/skill.zip" rs -x '*.DS_Store'
)

(
  cd "$RELEASES"
  shasum -a 256 rs-* skill.zip > SHA256SUMS
)

echo "release v$VERSION written to $RELEASES"
