#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE="github.com/Duang777/waybill-guardian"

cd "$ROOT_DIR"

check_imports() {
	local package="$1"
	local allowed_internal="$2"
	local imports
	imports="$(go list -f '{{range .Imports}}{{println .}}{{end}}' "$package")"

	while IFS= read -r dependency; do
		[[ -z "$dependency" ]] && continue
		if [[ "$dependency" == "$allowed_internal" ]]; then
			continue
		fi
		if [[ "$dependency" == "$MODULE/internal/delivery/"* ]]; then
			printf '%s must not import delivery package %s\n' "$package" "$dependency" >&2
			return 1
		fi
		if [[ "$dependency" == *.* ]]; then
			printf '%s must not import third-party package %s\n' "$package" "$dependency" >&2
			return 1
		fi
	done <<<"$imports"
}

check_imports "$MODULE/internal/delivery/domain" ""
check_imports \
	"$MODULE/internal/delivery/validate" \
	"$MODULE/internal/delivery/domain"

if rg -n --glob='*.go' --glob='!*_test.go' \
	'"github.com/Duang777/waybill-guardian/internal/delivery/(solve|artifact|service|execution)' \
	internal/delivery/validate; then
	echo "Independent Validator imports a forbidden delivery implementation package." >&2
	exit 1
fi

echo "Delivery package boundaries are valid."
