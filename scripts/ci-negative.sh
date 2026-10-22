#!/usr/bin/env bash
# Prove the test and lint gates fail closed when a bad change is injected.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cp -a "$root/services/hello-ledger" "$tmp/hello-ledger"
cat > "$tmp/hello-ledger/internal/ledger/negative_test.go" << 'EOF'
package ledger

import "testing"

func TestInjectedFailure(t *testing.T) {
	t.Fatal("injected lint/test error")
}
EOF

set +e
(cd "$tmp/hello-ledger" && go test ./internal/ledger/)
code=$?
set -e
if [ "$code" -eq 0 ]; then
  echo "injected test failure was not detected" >&2
  exit 1
fi
echo "injected test failed as expected (exit $code)"

printf 'package main\n  func main(){}\n' > "$tmp/bad.go"
if [ -z "$(gofmt -l "$tmp/bad.go")" ]; then
  echo "gofmt did not flag the injected formatting error" >&2
  exit 1
fi
echo "injected formatting error was flagged"
