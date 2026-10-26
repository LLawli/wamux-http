#!/usr/bin/env bash
# Refreshes a pinned contract. A refresh is a change of its own, with its
# CHANGELOG entry and whatever code it forces.
#
# usage: scripts/sync-contracts.sh wamux vX.Y.Z   the core's gRPC contract at a tag
#        scripts/sync-contracts.sh waha            WAHA's OpenAPI document
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "sync-contracts: $*" >&2; exit 1; }

case "${1:-}" in
  wamux)
    tag=${2:?usage: scripts/sync-contracts.sh wamux vX.Y.Z}
    commit=$(gh api "repos/LLawli/wamux/commits/$tag" --jq .sha) || die "no tag $tag in LLawli/wamux"
    files=$(gh api "repos/LLawli/wamux/contents/crates/wamux-proto/proto?ref=$commit" --jq '.[] | select(.name | endswith(".proto")) | .name')
    [ -n "$files" ] || die "no .proto files at $tag"
    rm -f proto/*.proto
    for f in $files; do
      gh api "repos/LLawli/wamux/contents/crates/wamux-proto/proto/$f?ref=$commit" --jq .content | base64 -d > "proto/$f"
    done
    echo "$tag $commit" > proto/WAMUX_VERSION
    go tool -modfile=tools/go.mod buf generate
    echo "proto/ is wamux $tag ($commit): $(echo "$files" | wc -l) files"
    ;;
  waha)
    curl -fsSL -o contract/waha-openapi.json https://waha.devlike.pro/swagger/openapi.json
    python3 -I -c 'import json,sys; d=json.load(open("contract/waha-openapi.json")); print("WAHA", d["info"]["version"])'
    ;;
  *)
    die "usage: scripts/sync-contracts.sh wamux vX.Y.Z | waha"
    ;;
esac
