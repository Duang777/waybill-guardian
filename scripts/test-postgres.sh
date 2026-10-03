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

cd "$ROOT_DIR"
env -u GOROOT go test -tags=integration -count=1 ./internal/storage/postgres
