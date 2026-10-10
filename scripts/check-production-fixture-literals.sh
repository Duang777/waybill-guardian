#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PATTERN='YD2026101001|杭州|成都|绵阳|川行快运|蜀道联运|精密电子元件'

cd "$ROOT_DIR"

set +e
matches="$(
	grep -rEnH \
		--include='*.go' \
		--include='*.ts' \
		--include='*.tsx' \
		--exclude='*_test.go' \
		--exclude='*.test.ts' \
		--exclude='*.test.tsx' \
		--exclude='test-fixture.ts' \
		--exclude-dir='datagenerate' \
		"$PATTERN" \
		cmd internal web/src
)"
status=$?
set -e

if ((status == 0)); then
	printf 'Production source contains embedded demo fixture values:\n%s\n' "$matches" >&2
	exit 1
fi
if ((status != 1)); then
	printf 'Fixture literal scan failed with status %s.\n' "$status" >&2
	exit "$status"
fi

printf 'Production source contains no known demo fixture literals.\n'
