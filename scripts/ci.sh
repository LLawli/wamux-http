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

step "vet"
go vet ./...

step "lint"
tool golangci-lint run ./...

step "test"
go test -race -count=1 ./...

step "coverage document"
go run ./cmd/coverage -check docs/coverage.md

step "vulnerabilities"
tool govulncheck ./...

printf '\nall gates passed\n'
