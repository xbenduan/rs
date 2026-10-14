#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' "$ROOT/server/server.go")"
TAG="v$VERSION"
RELEASES="$ROOT/releases"
cd "$ROOT"

if [[ -z "$VERSION" ]]; then
  echo "error: cannot read version from server/server.go" >&2
  exit 1
fi
escaped_version="${VERSION//./\\.}"
if ! grep -qE "^## v${escaped_version}([[:space:]]|$)" "$ROOT/CHANGELOG.md"; then
  echo "error: CHANGELOG.md has no v$VERSION entry" >&2
  exit 1
fi

release_notes="$(mktemp "${TMPDIR:-/tmp}/rs-release-notes.XXXXXX")"
trap 'rm -f "$release_notes"' EXIT
awk -v tag="$TAG" '
  index($0, "## " tag " ") == 1 || $0 == "## " tag {
    found = 1
    next
  }
  found && /^## / { exit }
  found { print }
' "$ROOT/CHANGELOG.md" > "$release_notes"
if ! grep -q '[^[:space:]]' "$release_notes"; then
  echo "error: CHANGELOG.md v$VERSION entry has no release notes" >&2
  exit 1
fi

for command in git gh zip shasum; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "error: $command is required" >&2
    exit 1
  fi
done
if ! gh auth status >/dev/null 2>&1; then
  echo "error: gh is not authenticated; run gh auth login" >&2
  exit 1
fi
if [[ -n "$(git -C "$ROOT" status --porcelain)" ]]; then
  echo "error: commit all source changes before releasing" >&2
  exit 1
fi
branch="$(git -C "$ROOT" branch --show-current)"
if [[ -z "$branch" ]]; then
  echo "error: cannot release from a detached HEAD" >&2
  exit 1
fi
commit="$(git -C "$ROOT" rev-parse HEAD)"
if gh release view "$TAG" >/dev/null 2>&1; then
  echo "error: GitHub Release $TAG already exists" >&2
  exit 1
fi

if ! git -C "$ROOT" rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  if git -C "$ROOT" ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1; then
    git -C "$ROOT" fetch --quiet origin "refs/tags/$TAG:refs/tags/$TAG"
  fi
fi
tag_exists=false
if git -C "$ROOT" rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  tag_exists=true
  tag_commit="$(git -C "$ROOT" rev-list -n 1 "$TAG")"
  if [[ "$tag_commit" != "$commit" ]]; then
    echo "error: tag $TAG points to $tag_commit, current commit is $commit" >&2
    exit 1
  fi
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
  shasum -a 256 -c SHA256SUMS
)

git -C "$ROOT" push origin "HEAD:refs/heads/$branch"
if [[ "$tag_exists" == false ]]; then
  git -C "$ROOT" tag -a "$TAG" -m "$TAG"
fi
git -C "$ROOT" push origin "refs/tags/$TAG"

gh release create "$TAG" "$RELEASES"/* \
  --verify-tag \
  --title "$TAG" \
  --notes-file "$release_notes" \
  --latest

release_url="$(gh release view "$TAG" --json url --jq .url)"
echo "release $TAG published: $release_url"
