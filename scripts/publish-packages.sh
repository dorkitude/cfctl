#!/usr/bin/env bash
# Publish a cfctl release to the Homebrew tap (dorkitude/homebrew-tap, as a
# cask) and the Scoop bucket (dorkitude/scoop-bucket). Both install the
# release's prebuilt binaries: the universal macOS binary, or the Linux or
# Windows one for the machine's architecture. Nothing is compiled on the
# user's machine, so Homebrew needs neither Go nor the Xcode Command Line
# Tools (it refuses to build formulae without them on macOS Tahoe).
#
# Run after the GitHub release for the tag exists (the release workflow builds
# it). Needs `gh` logged in with push access to both repos. Idempotent: if the
# formula and manifest already match the tag, nothing is committed.
#
#   scripts/publish-packages.sh v0.1.100
#   DRY_RUN=1 scripts/publish-packages.sh v0.1.100   # print, don't push
set -euo pipefail

TAG="${1:?usage: $0 vX.Y.Z}"
VERSION="${TAG#v}"
REPO="dorkitude/cfctl"
DESC="The whole Cloudflare platform from your terminal (Workers, R2, DNS, every API operation)"
HOMEPAGE="https://github.com/${REPO}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> Fetching release assets for ${TAG}"
gh release download "$TAG" -R "$REPO" -p checksums.txt -D "$WORK"
sha_of() { awk -v f="$1" '$2 == f {print $1}' "$WORK/checksums.txt"; }
WIN_AMD64="cfctl_${VERSION}_windows_amd64.zip"
WIN_ARM64="cfctl_${VERSION}_windows_arm64.zip"
WIN_AMD64_SHA="$(sha_of "$WIN_AMD64")"
WIN_ARM64_SHA="$(sha_of "$WIN_ARM64")"
[ -n "$WIN_AMD64_SHA" ] && [ -n "$WIN_ARM64_SHA" ] || { echo "Windows zips missing from checksums.txt" >&2; exit 1; }

asset() { echo "cfctl_${VERSION}_$1.tar.gz"; }
url_of() { echo "https://github.com/${REPO}/releases/download/${TAG}/$(asset "$1")"; }
for target in darwin_all linux_arm64 linux_amd64; do
  [ -n "$(sha_of "$(asset "$target")")" ] || { echo "$(asset "$target") missing from checksums.txt" >&2; exit 1; }
done

publish() { # repo, path, file, message; an empty file removes path
  local repo="$1" path="$2" src="$3" msg="$4" dir="$WORK/$(basename "$1")"
  [ -d "$dir" ] || gh repo clone "$repo" "$dir" -- -q --depth 1
  if [ -z "$src" ]; then
    if ! git -C "$dir" ls-files --error-unmatch "$path" >/dev/null 2>&1; then
      echo "    $repo: $path already gone"
      return
    fi
    if [ -n "${DRY_RUN:-}" ]; then
      echo "    [dry run] would remove $path from $repo"; return
    fi
    git -C "$dir" rm -q "$path"
    git -C "$dir" commit -q -m "$msg"
    git -C "$dir" push -q origin HEAD
    echo "    $repo: removed $path"
    return
  fi
  mkdir -p "$(dirname "$dir/$path")"
  cp "$src" "$dir/$path"
  if git -C "$dir" diff --quiet -- "$path" && git -C "$dir" ls-files --error-unmatch "$path" >/dev/null 2>&1; then
    echo "    $repo: $path already up to date"
    return
  fi
  if [ -n "${DRY_RUN:-}" ]; then
    echo "    [dry run] would commit $path to $repo:"; cat "$src"; return
  fi
  git -C "$dir" add "$path"
  git -C "$dir" commit -q -m "$msg"
  git -C "$dir" push -q origin HEAD
  echo "    $repo: pushed $path"
}

echo "==> Homebrew cask"
cat > "$WORK/cfctl.rb" <<EOF
cask "cfctl" do
  version "${VERSION}"

  on_macos do
    url "$(url_of darwin_all)"
    sha256 "$(sha_of "$(asset darwin_all)")"
  end

  on_linux do
    on_arm do
      url "$(url_of linux_arm64)"
      sha256 "$(sha_of "$(asset linux_arm64)")"
    end
    on_intel do
      url "$(url_of linux_amd64)"
      sha256 "$(sha_of "$(asset linux_amd64)")"
    end
  end

  name "cfctl"
  desc "${DESC}"
  homepage "${HOMEPAGE}"

  livecheck do
    skip "Updated by scripts/publish-packages.sh on release."
  end

  binary "cfctl"

  # The binary is not notarized: without this, macOS quarantines it and
  # refuses to run it. No-op on Linux.
  postflight_steps do
    run "/usr/bin/xattr", args: ["-d", "com.apple.quarantine", "{{staged_path}}/cfctl"], must_succeed: false
  end
end
EOF
publish dorkitude/homebrew-tap Casks/cfctl.rb "$WORK/cfctl.rb" "cfctl ${VERSION}"
# The formula built from source, which Homebrew refuses without the Xcode
# Command Line Tools on macOS Tahoe. The cask replaces it.
publish dorkitude/homebrew-tap Formula/cfctl.rb "" "cfctl: replaced by the cask"

echo "==> Scoop manifest"
cat > "$WORK/cfctl.json" <<EOF
{
  "version": "${VERSION}",
  "description": "${DESC}",
  "homepage": "${HOMEPAGE}",
  "license": "MIT",
  "architecture": {
    "64bit": {
      "url": "https://github.com/${REPO}/releases/download/${TAG}/${WIN_AMD64}",
      "hash": "${WIN_AMD64_SHA}",
      "bin": "cfctl.exe"
    },
    "arm64": {
      "url": "https://github.com/${REPO}/releases/download/${TAG}/${WIN_ARM64}",
      "hash": "${WIN_ARM64_SHA}",
      "bin": "cfctl.exe"
    }
  },
  "checkver": {
    "github": "${HOMEPAGE}"
  },
  "autoupdate": {
    "architecture": {
      "64bit": {
        "url": "https://github.com/${REPO}/releases/download/v\$version/cfctl_\$version_windows_amd64.zip"
      },
      "arm64": {
        "url": "https://github.com/${REPO}/releases/download/v\$version/cfctl_\$version_windows_arm64.zip"
      }
    }
  }
}
EOF
python3 -m json.tool "$WORK/cfctl.json" >/dev/null
publish dorkitude/scoop-bucket cfctl.json "$WORK/cfctl.json" "cfctl ${VERSION}"

echo "==> Done: brew install dorkitude/tap/cfctl · scoop install dorkitude/cfctl"
