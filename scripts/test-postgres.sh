#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${POSTGRES_TEST_IMAGE:-postgres:17-alpine}"
CONTAINER="waybill-postgres-test-$$"

cleanup() {
	docker rm --force "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run --detach --rm \
	--name "$CONTAINER" \
	--env POSTGRES_USER=waybill \
	--env POSTGRES_PASSWORD=waybill \
	--env POSTGRES_DB=waybill \
	--publish 127.0.0.1::5432 \
	"$IMAGE" >/dev/null

for _ in $(seq 1 120); do
	if docker exec "$CONTAINER" pg_isready --host 127.0.0.1 --username waybill --dbname waybill >/dev/null 2>&1; then
		break
	fi
	sleep 0.25
done
if ! docker exec "$CONTAINER" pg_isready --host 127.0.0.1 --username waybill --dbname waybill >/dev/null 2>&1; then
	echo "PostgreSQL test container did not become ready." >&2
	exit 1
fi

PORT_MAPPING="$(docker port "$CONTAINER" 5432/tcp)"
PORT="${PORT_MAPPING##*:}"
export TEST_DATABASE_URL="postgres://waybill:waybill@127.0.0.1:${PORT}/waybill?sslmode=disable"
export DATABASE_URL="$TEST_DATABASE_URL"

cd "$ROOT_DIR"
env -u GOROOT go test -tags=integration -count=1 ./internal/storage/postgres

if [[ "${SKIP_POSTGRES_E2E:-0}" != "1" ]]; then
	require_command() {
		if ! command -v "$1" >/dev/null 2>&1; then
			printf 'Missing required command for PostgreSQL E2E: %s\n' "$1" >&2
			exit 1
		fi
	}
	require_command npm
	export STORAGE=postgres
	export PLATFORM=mock
	export TENANT_ID="e2e-$CONTAINER"
	export CHECKPOINT_KEY_ID="test-v1"
	export CHECKPOINT_ENCRYPTION_KEY="MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	export E2E_BACKEND_PORT="${E2E_BACKEND_PORT:-18211}"
	export E2E_WEB_PORT="${E2E_WEB_PORT:-15211}"
	npm --prefix "$ROOT_DIR/web" run verify:e2e
fi
