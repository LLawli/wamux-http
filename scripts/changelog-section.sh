#!/usr/bin/env bash
# Prints the CHANGELOG.md section of one version, without its heading.
# Fails when the section is missing or empty, so a release never ships
# with blank notes.
#
# usage: scripts/changelog-section.sh X.Y.Z
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:?usage: scripts/changelog-section.sh X.Y.Z}
section=$(awk -v hdr="## [$version]" '
  index($0, hdr) == 1 { flag = 1; next }
  flag && index($0, "## [") == 1 { exit }
  flag && /^\[[^]]+\]: / { next }
  flag { print }
' CHANGELOG.md)
if [ -z "$(printf '%s' "$section" | tr -d '[:space:]')" ]; then
  echo "changelog-section: CHANGELOG.md has no section for $version" >&2
  exit 1
fi
printf '%s\n' "$section"
