# Delivery benchmark commands

This directory contains the executable evidence pipeline for city delivery planning. The pipeline
rebuilds fixed datasets, runs configured planners, validates every plan, repeats each configuration
20 times, and generates Markdown from one JSON report.

## Rebuild the datasets

Run:

```bash
go run ./scripts/delivery-benchmark generate
```

The command rebuilds every dataset from
[`config.json`](./config.json) and compares the result with
[`manifest.json`](../../data/simulated/delivery-benchmark-v1/manifest.json). The check covers the
generator version, seed, field sources, units, rounding rules, canonical byte digest, problem
digest, and reference plan digest.

To inspect the canonical problem documents without committing them, run:

```bash
go run ./scripts/delivery-benchmark generate \
  --output-dir /tmp/waybill-delivery-benchmark
```

Run `generate --write` only after an intentional generator or dataset change. Review every digest
change before committing the manifest.

## Generate evidence

Run:

```bash
./scripts/delivery-benchmark/run.sh
```

The command runs the embedded reference baseline and every configured command adapter. It writes:

- [`delivery-benchmark.v1.json`](../../docs/reports/delivery-benchmark.v1.json), the authoritative
  result.
- [`delivery-benchmark.md`](../../docs/reports/delivery-benchmark.md), the generated Chinese view.
- [`delivery-benchmark.en.md`](../../docs/reports/delivery-benchmark.en.md), the generated English
  view.

An unavailable adapter has the status `blocked`. A failed command, invalid plan, hard violation, or
digest change has the status `failed`. Neither status counts as a pass.

## Connect a solver

A command adapter reads its command from the environment variable named in `config.json`. The
value is a JSON string array, not a shell command. This avoids shell expansion and preserves
arguments that contain spaces.

The command must contain `{problem}` and `{plan}`. The runner also replaces `{dataset}`, `{budget}`,
and `{timeout_seconds}`.

Example:

```bash
export DELIVERY_BENCHMARK_BUILTIN_COMMAND='[
  "./bin/delivery-solver",
  "solve",
  "--problem", "{problem}",
  "--plan", "{plan}",
  "--budget", "{budget}"
]'

./scripts/delivery-benchmark/run.sh --require-publication
```

The command must write one strict `delivery.plan.v1` JSON document to `{plan}`. The runner invokes
the command separately for each replay. It then calls `internal/delivery/validate` and requires:

- `valid=true`
- zero error-severity violations
- one stable plan digest
- one stable validation report digest
- exactly 20 completed replays

`--require-publication` fails while a required adapter is blocked or failed.

## Check committed evidence

Run:

```bash
./scripts/delivery-benchmark/check.sh
```

This command runs the benchmark tool tests, rebuilds the manifest, validates the JSON report,
regenerates both Markdown documents in memory, checks local links and anchors, and verifies every
test name cited by the public READMEs.

To probe external HTTP links, run:

```bash
./scripts/delivery-benchmark/check.sh --external
```

The external check accepts successful redirects and authenticated or rate-limited endpoints. It
fails on unreachable endpoints and HTTP 4xx or 5xx responses other than 401, 403, and 429.
