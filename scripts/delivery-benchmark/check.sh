#!/usr/bin/env bash

set -Eeuo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root_dir"

go test ./scripts/delivery-benchmark
go run ./scripts/delivery-benchmark check "$@"
