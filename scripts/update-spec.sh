#!/usr/bin/env bash
# update-spec.sh: refresh the vendored Cloudflare OpenAPI spec.
#
# Shallow-clones github.com/cloudflare/api-schemas (or uses the checkout given
# as $1), vendors openapi.json into internal/apispec/openapi.json.gz (minified,
# gzipped), records the source commit + date in spec-source.json, and
# regenerates internal/apispec/ops_gen.go. Idempotent: re-running with the
# same upstream commit produces no diff.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
src="${1:-}"
if [[ -z "$src" ]]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  git clone --quiet --depth 1 https://github.com/cloudflare/api-schemas "$tmp/api-schemas"
  src="$tmp/api-schemas"
fi

cd "$repo_root/internal/apispec"
go run ./gen -import "$src"
cd "$repo_root"
go test ./internal/apispec/ ./cmd/ -run 'TestTableBasics|TestCoverage'
git --no-pager diff --stat -- internal/apispec
