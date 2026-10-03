#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HISTORY_SCAN_DIR_EXPLICIT="${HISTORY_SCAN_DIR+x}"
HISTORY_SCAN_DIR="${HISTORY_SCAN_DIR:-${DATA_DIR:-$ROOT_DIR/data}/hastekit}"
PROHIBITED_PATTERN='13800001234|13961234567|川A8X6Q2|"(shipper_phone|phone|plate|license_plate|longitude|latitude|template_id|params)"[[:space:]]*:'
failed=0
history_files=0
database_history_rows=0

if [[ -d "$HISTORY_SCAN_DIR" ]]; then
	while IFS= read -r -d '' file; do
		history_files=$((history_files + 1))
		if grep -En "$PROHIBITED_PATTERN" "$file"; then
			printf 'Prohibited history data found in %s\n' "$file" >&2
			failed=1
		fi
	done < <(find "$HISTORY_SCAN_DIR" -type f -name '*.jsonl' -print0)
elif [[ -n "$HISTORY_SCAN_DIR_EXPLICIT" || -z "${DATABASE_URL:-}" ]]; then
	printf 'History scan directory does not exist: %s\n' "$HISTORY_SCAN_DIR" >&2
	exit 1
fi
if [[ "$history_files" -eq 0 && -z "${DATABASE_URL:-}" ]]; then
	printf 'No JSONL history files found in %s\n' "$HISTORY_SCAN_DIR" >&2
	exit 1
fi

if [[ -n "${DATABASE_URL:-}" ]]; then
	if ! command -v psql >/dev/null 2>&1; then
		echo "DATABASE_URL is set, but psql is unavailable." >&2
		exit 1
	fi
	metadata_columns="$(
		psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -Atc "
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'waybill'
			  AND table_name IN ('agent_checkpoints', 'agent_summaries')
			  AND data_type IN ('json', 'jsonb')
		"
	)"
	if [[ "$metadata_columns" != "0" ]]; then
		echo "PostgreSQL agent history still has plaintext JSON columns." >&2
		failed=1
	fi

	database_history_rows="$(
		psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -Atc "
			SELECT
				(SELECT count(*) FROM waybill.agent_checkpoints) +
				(SELECT count(*) FROM waybill.agent_summaries)
		"
	)"
	plaintext="$(
		psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -Atc "
			SELECT concat_ws('|', tenant_id, namespace, thread_id, sdk_run_id,
				COALESCE(previous_sdk_run_id, ''), conversation_id,
				payload_hash, encryption_key_id, group_id,
				privacy_schema_version::text)
			FROM waybill.agent_checkpoints
			UNION ALL
			SELECT concat_ws('|', tenant_id, namespace, thread_id, summary_id,
				payload_hash, privacy_schema_version::text)
			FROM waybill.agent_summaries
		"
	)"
	if grep -Eq "$PROHIBITED_PATTERN" <<<"$plaintext"; then
		echo "Prohibited history data found in PostgreSQL plaintext columns." >&2
		failed=1
	fi
fi

if [[ $((history_files + database_history_rows)) -eq 0 ]]; then
	echo "No history artifacts found." >&2
	exit 1
fi
if [[ "$failed" -ne 0 ]]; then
	exit 1
fi

echo "History governance scan passed."
