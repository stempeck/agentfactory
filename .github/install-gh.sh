#!/usr/bin/env bash
# Installs the GitHub CLI release quickstart.sh pins for the advisory-decay job's filing step: the
# runners carry no gh. The version and both checksums are read from quickstart.sh, never copied here,
# so a heal that moves the gh pin moves this install with it. Nothing outlives the job: the binary
# lands under $RUNNER_TEMP and only later steps see it, through $GITHUB_PATH.
set -euo pipefail

die() {
  echo "ERROR: install-gh: $*" >&2
  exit 1
}

[ -n "${RUNNER_TEMP:-}" ] || die "RUNNER_TEMP is not set"
[ -n "${GITHUB_PATH:-}" ] || die "GITHUB_PATH is not set"

qs="$(cd "$(dirname "$0")/.." && pwd)/quickstart.sh"
[ -f "$qs" ] || die "no quickstart.sh at $qs"

# pin NAME prints the literal value of a NAME="..." line of quickstart.sh, which must appear exactly once.
pin() {
  local lines
  lines=$(grep -E "^$1=\"[^\"\$]*\"\$" "$qs" || true)
  if [ -z "$lines" ] || [ "$(printf '%s\n' "$lines" | wc -l)" -ne 1 ]; then
    die "quickstart.sh does not declare $1=\"...\" exactly once"
  fi
  lines=${lines#*=\"}
  printf '%s' "${lines%\"}"
}

version=$(pin GH_VERSION) || exit 1
sha_arm64=$(pin GH_SHA256_ARM64) || exit 1
sha_amd64=$(pin GH_SHA256_AMD64) || exit 1
[[ $version =~ ^[0-9]+(\.[0-9]+)+$ ]] || die "GH_VERSION \"$version\" is not a release number"
for sha in "$sha_arm64" "$sha_amd64"; do
  [[ $sha =~ ^[0-9a-f]{64}$ ]] || die "\"$sha\" is not a lowercase sha256"
done

machine=$(uname -m)
case "$machine" in
  aarch64 | arm64) arch=arm64 sha=$sha_arm64 ;;
  x86_64 | amd64) arch=amd64 sha=$sha_amd64 ;;
  *) die "gh $version: unsupported architecture $machine" ;;
esac

stage=$(mktemp -d "$RUNNER_TEMP/gh-download.XXXXXX")
trap 'rm -rf "$stage"' EXIT
tarball="gh_${version}_linux_${arch}.tar.gz"
curl -fsSL --connect-timeout 30 --max-time 300 "https://github.com/cli/cli/releases/download/v${version}/${tarball}" -o "$stage/$tarball" ||
  die "gh $version: download failed"
printf '%s  %s\n' "$sha" "$stage/$tarball" | sha256sum --check --strict > /dev/null ||
  die "gh $version: $tarball does not match its pinned sha256; not installing"
tar -C "$stage" -xzf "$stage/$tarball" || die "gh $version: cannot unpack $tarball"
staged="$stage/gh_${version}_linux_${arch}/bin/gh"
reported=$("$staged" --version | awk 'NR == 1 { print $3 }') || die "gh $version: the unpacked binary does not run"
[ "$reported" = "$version" ] || die "gh $version: the unpacked binary reports version \"$reported\""

bin="$RUNNER_TEMP/gh-$version/bin"
mkdir -p "$bin"
install -m 0755 "$staged" "$bin/gh"
echo "$bin" >> "$GITHUB_PATH"
echo "install-gh: gh $version ($arch) installed in $bin"
