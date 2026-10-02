#!/usr/bin/env bash
# Tag a vink release. The tag starts the release workflow, which runs goreleaser: binaries for
# every platform, sha256sums.txt, SBOMs and the container image. When the Homebrew tap has a
# vink formula, its url and sha256 are bumped as well.
#   scripts/release.sh 0.1.0          (macOS: uses BSD sed -i '')
# Requires: a clean tree on main pushed to origin, and ssh access to the repos.
set -euo pipefail
ver=${1:?usage: scripts/release.sh <version>   e.g. 0.1.0}
ver=${ver#v}
R=$(cd "$(dirname "$0")/.." && pwd)
cd "$R"
[ "$(git branch --show-current)" = main ] || { echo "release.sh: not on main" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "release.sh: working tree not clean" >&2; exit 1; }
git fetch -q origin
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "release.sh: main is not pushed" >&2; exit 1; }
# release notes come from CHANGELOG.md: the "## <ver> (date)" section must exist before tagging
notes=$(awk -v v="$ver" '/^## /{p = ($2 == v)} p && !/^## /' CHANGELOG.md | sed '/./,$!d')
[ -n "$notes" ] || { echo "release.sh: CHANGELOG.md has no \"## $ver (YYYY-MM-DD)\" section; write the release notes first (move the Unreleased items)" >&2; exit 1; }
grep -qE "^## $ver \([0-9]{4}-[0-9]{2}-[0-9]{2}\)" CHANGELOG.md || { echo "release.sh: the CHANGELOG.md heading must be \"## $ver (YYYY-MM-DD)\"" >&2; exit 1; }
go vet ./... && go test ./... >/dev/null && echo "tests ok"

git tag -a "v$ver" -m "vink v$ver"
git push -q origin "v$ver"
echo "tag v$ver pushed; the release workflow builds the assets and the image"
echo "release: https://github.com/w4jnl/vink/releases/tag/v$ver"
echo "CI:      https://github.com/w4jnl/vink/actions"

TAP=${VINK_TAP_DIR:-$R/../homebrew-tap}
[ -d "$TAP/.git" ] || git clone -q git@github.com:w4jnl/homebrew-tap.git "$TAP"
git -C "$TAP" pull -q --ff-only
f=$TAP/Formula/vink.rb
if [ ! -f "$f" ]; then
  echo "no Formula/vink.rb in the tap yet; nothing to bump"
  exit 0
fi
url="https://github.com/w4jnl/vink/archive/refs/tags/v$ver.tar.gz"
sha=""
for _ in $(seq 1 30); do
  if sha=$(curl -sfL "$url" | shasum -a 256 | cut -d' ' -f1) && [ ${#sha} -eq 64 ]; then break; fi
  sha=""; sleep 2
done
[ -n "$sha" ] || { echo "release.sh: $url not available yet; rerun the tap bump by hand" >&2; exit 1; }
sed -i '' -e "s#^  url \".*\"#  url \"$url\"#" -e "s#^  sha256 \".*\"#  sha256 \"$sha\"#" "$f"
git -C "$TAP" add Formula/vink.rb
git -C "$TAP" commit -q -m "vink $ver"
git -C "$TAP" push -q
echo "tap bumped: $f"
echo "users: brew update && brew upgrade vink"
