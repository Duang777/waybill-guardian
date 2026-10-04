#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$ROOT_DIR/web"
BACKEND_HOST="${BACKEND_HOST:-127.0.0.1}"
BACKEND_PORT="${BACKEND_PORT:-8080}"
WEB_HOST="${WEB_HOST:-127.0.0.1}"
WEB_PORT="${WEB_PORT:-5173}"
DATA_DIR="${DATA_DIR:-$ROOT_DIR/data}"
DATA_FILE="${DATA_FILE:-}"
AGENT_MODE="${AGENT_MODE:-demo}"
PLATFORM="${PLATFORM:-mock}"
API_URL="http://$BACKEND_HOST:$BACKEND_PORT"
WEB_URL="http://$WEB_HOST:$WEB_PORT"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/waybill-guardian.XXXXXX")"
BACKEND_PID=""
WEB_PID=""

cleanup() {
	local status=$?
	trap - EXIT
	for pid in "$WEB_PID" "$BACKEND_PID"; do
		if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
		fi
	done
	for pid in "$WEB_PID" "$BACKEND_PID"; do
		if [[ -n "$pid" ]]; then
			wait "$pid" 2>/dev/null || true
		fi
	done
	rm -rf "$TEMP_DIR"
	exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

require_command() {
	if ! command -v "$1" >/dev/null 2>&1; then
		printf 'Missing required command: %s\n' "$1" >&2
		exit 1
	fi
}

wait_for_http() {
	local url=$1
	local pid=$2
	local label=$3
	local attempt=0

	while ((attempt < 120)); do
		if curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
			return
		fi
		if ! kill -0 "$pid" 2>/dev/null; then
			set +e
			wait "$pid"
			local status=$?
			set -e
			printf '%s exited during startup with status %s.\n' "$label" "$status" >&2
			if ((status == 0)); then
				exit 1
			fi
			exit "$status"
		fi
		attempt=$((attempt + 1))
		sleep 0.25
	done

	printf 'Timed out waiting for %s at %s.\n' "$label" "$url" >&2
	exit 1
}

require_command go
require_command npm
require_command curl

if [[ "$PLATFORM" == "file" && -z "$DATA_FILE" ]]; then
	printf 'DATA_FILE is required when PLATFORM=file.\n' >&2
	exit 1
fi

if [[ ! -x "$WEB_DIR/node_modules/.bin/vite" ]]; then
	printf 'Installing web dependencies with npm ci...\n'
	(cd "$WEB_DIR" && npm ci)
fi

printf 'Building the Go server...\n'
(cd "$ROOT_DIR" && env -u GOROOT go build -o "$TEMP_DIR/waybill-guardian" ./cmd/server)

printf 'Starting API at %s...\n' "$API_URL"
(
	cd "$ROOT_DIR"
	exec env \
		AGENT_MODE="$AGENT_MODE" \
		DATA_FILE="$DATA_FILE" \
		PLATFORM="$PLATFORM" \
		DATA_DIR="$DATA_DIR" \
		HTTP_ADDR="$BACKEND_HOST:$BACKEND_PORT" \
		"$TEMP_DIR/waybill-guardian"
) &
BACKEND_PID=$!
wait_for_http "$API_URL/healthz" "$BACKEND_PID" "API"

printf 'Starting web console at %s...\n' "$WEB_URL"
(
	cd "$WEB_DIR"
	exec env VITE_API_TARGET="$API_URL" \
		./node_modules/.bin/vite \
		--host "$WEB_HOST" \
		--port "$WEB_PORT" \
		--strictPort
) &
WEB_PID=$!
wait_for_http "$WEB_URL" "$WEB_PID" "web console"

printf '\nWaybill Guardian is ready: %s\n' "$WEB_URL"
printf 'Press Ctrl+C to stop both processes.\n'

while true; do
	if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
		set +e
		wait "$BACKEND_PID"
		status=$?
		set -e
		printf 'API process stopped with status %s.\n' "$status" >&2
		if ((status == 0)); then
			exit 1
		fi
		exit "$status"
	fi
	if ! kill -0 "$WEB_PID" 2>/dev/null; then
		set +e
		wait "$WEB_PID"
		status=$?
		set -e
		printf 'Web process stopped with status %s.\n' "$status" >&2
		if ((status == 0)); then
			exit 1
		fi
		exit "$status"
	fi
	sleep 1
done
