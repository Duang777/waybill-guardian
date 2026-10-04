#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mode="${1:---check}"
go_licenses_version="v2.0.1"
web_checker_version="4.4.2"

case "$mode" in
  --check | --write) ;;
  *)
    echo "usage: $0 [--check|--write]" >&2
    exit 2
    ;;
esac

for command in go npm; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required" >&2
    exit 1
  fi
done

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

go_tool_dir="$tmp_dir/go-bin"
mkdir -p "$go_tool_dir"
env -u GOROOT GOBIN="$go_tool_dir" \
  go install "github.com/google/go-licenses/v2@${go_licenses_version}"

(
  cd "$root_dir"
  GOOS=linux GOARCH=amd64 "$go_tool_dir/go-licenses" report ./...
) >"$tmp_dir/go.raw.csv"
LC_ALL=C sort "$tmp_dir/go.raw.csv" >"$tmp_dir/go.csv"

(
  cd "$root_dir"
  GOOS=linux GOARCH=amd64 "$go_tool_dir/go-licenses" \
    check ./... --disallowed_types=forbidden,restricted
)

(
  cd "$root_dir/web"
  npm ci --ignore-scripts --no-audit --no-fund >&2
  npx --yes "license-checker-rseidelsohn@${web_checker_version}" \
    --production \
    --csv \
    --excludePrivatePackages \
    --relativeModulePath \
    --relativeLicensePath \
    --start .
) >"$tmp_dir/web.raw.csv"

{
  head -n 1 "$tmp_dir/web.raw.csv"
  tail -n +2 "$tmp_dir/web.raw.csv" | LC_ALL=C sort
} >"$tmp_dir/web.csv"

if [[ ! -s "$tmp_dir/go.csv" ]] || [[ "$(wc -l <"$tmp_dir/web.csv")" -le 1 ]]; then
  echo "license scan returned an empty dependency list" >&2
  exit 1
fi

if grep -Eiq '(^|[^[:alpha:]])(A?GPL|LGPL)([^[:alpha:]]|$)|(^|,)Unknown$|\"(UNKNOWN|UNLICENSED)\"' \
  "$tmp_dir/go.csv" "$tmp_dir/web.csv"; then
  echo "license scan found GPL, AGPL, LGPL, unknown, or unlicensed dependencies" >&2
  exit 1
fi

go_manifest="$root_dir/docs/licenses/go.csv"
web_manifest="$root_dir/docs/licenses/web.csv"

if [[ "$mode" == "--write" ]]; then
  mkdir -p "$root_dir/docs/licenses"
  cp "$tmp_dir/go.csv" "$go_manifest"
  cp "$tmp_dir/web.csv" "$web_manifest"
  echo "updated docs/licenses/go.csv and docs/licenses/web.csv"
  exit 0
fi

for manifest in "$go_manifest" "$web_manifest"; do
  if [[ ! -s "$manifest" ]]; then
    echo "missing license manifest: ${manifest#"$root_dir/"}" >&2
    echo "run ./scripts/licenses.sh --write" >&2
    exit 1
  fi
done

if ! cmp -s "$go_manifest" "$tmp_dir/go.csv"; then
  diff -u "$go_manifest" "$tmp_dir/go.csv" || true
  echo "docs/licenses/go.csv is stale; run ./scripts/licenses.sh --write" >&2
  exit 1
fi

if ! cmp -s "$web_manifest" "$tmp_dir/web.csv"; then
  diff -u "$web_manifest" "$tmp_dir/web.csv" || true
  echo "docs/licenses/web.csv is stale; run ./scripts/licenses.sh --write" >&2
  exit 1
fi

echo "license manifests are current and contain no disallowed licenses"
