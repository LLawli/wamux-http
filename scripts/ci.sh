#!/usr/bin/env bash
# Every gate, in the order CI runs them. A green run here is a green CI run.
set -euo pipefail
cd "$(dirname "$0")/.."

tool() { go tool -modfile=tools/go.mod "$@"; }
step() { printf '\n== %s\n' "$*"; }

step "format"
unformatted=$(gofmt -l cmd contract internal)
if [ -n "$unformatted" ]; then
  echo "gofmt needed:"; echo "$unformatted"; exit 1
fi

step "generated gRPC code matches proto/"
gen=$(mktemp -d)
trap 'rm -rf "$gen"' EXIT
sed "s#out: internal/core/wamuxv1#out: $gen#" buf.gen.yaml > "$gen/buf.gen.yaml"
tool buf generate --template "$gen/buf.gen.yaml"
rm "$gen/buf.gen.yaml"
diff -r "$gen" internal/core/wamuxv1 >/dev/null || { echo "internal/core/wamuxv1 is stale: run 'go tool -modfile=tools/go.mod buf generate'"; exit 1; }

step "workflows"
# actionlint runs shellcheck on each run: block when shellcheck works here.
if shellcheck --version >/dev/null 2>&1; then
  go tool -modfile=tools/actionlint/go.mod actionlint
else
  go tool -modfile=tools/actionlint/go.mod actionlint -shellcheck=
fi

step "vet"
go vet ./...

step "lint"
tool golangci-lint run ./...

step "test"
go test -race -count=1 ./...

step "third-party licenses"
tpl=$(mktemp)
scripts/third-party-licenses.sh "$tpl"
cmp -s "$tpl" THIRD-PARTY-LICENSES.md || { rm -f "$tpl"; echo "THIRD-PARTY-LICENSES.md is stale: run scripts/third-party-licenses.sh"; exit 1; }
rm -f "$tpl"

step "coverage document"
go run ./cmd/coverage -check docs/coverage.md

step "vulnerabilities"
tool govulncheck ./...

printf '\nall gates passed\n'
