# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

PIMS (Patient Information Management System) is a demo that showcases Confluent Cloud (CC) **Tableflow** with a **Debezium** CDC connector, materializing MySQL tables (patients, visits, appointments) as Iceberg tables. It is a **Wails v2 (Go + React/TypeScript) desktop app** that:

1. Fully automates setup of every infrastructure component.
2. Simulates realistic patient registrations, visits and appointments.
3. Shows how fresh the Iceberg tables are, charting Tableflow's own freshness metrics.

`docs/specs/scope.md` is the source of truth for intended behavior. `README.md` has the operator-facing overview.

## Commands

Run these from `demos/pims`; this project lives in the `bds-cflt-assets` monorepo.

```sh
wails dev                                   # run the app with live reload
wails build                                 # build/bin/pims.app
go vet ./... && go test -race ./...          # all Go unit tests
go test -run TestSimulationInvariants ./internal/sim   # a single Go test
cd frontend && npx tsc --noEmit             # type-check the UI
wails generate module                       # regenerate frontend/wailsjs after changing bound Go methods/types
```

Opt-in integration tests (skipped by default):
- `PIMS_INTEGRATION=1 go test ./internal/terraform` downloads terraform and the Confluent provider and runs `terraform validate`.
- `PIMS_TEST_MYSQL_ROOT_PASSWORD=… PIMS_TEST_MYSQL_PORT=… go test ./internal/schema ./internal/sim` run against a real MySQL 8.4 (see README for the `docker run` line).
- Compose and DuckDB tests run automatically when Docker and the `pims-duckdb` image are available.

## Architecture

Data path: `MySQL → Debezium (Kafka Connect, local) → CC Kafka topics (Avro + Schema Registry) → Tableflow (Iceberg, managed storage)`

Freshness path: `Tableflow → CC Metrics API → PIMS`. PIMS polls `io.confluent.tableflow/event_freshness_latency_milliseconds` and `…/processing_freshness_latency_milliseconds` once a minute and charts them. It does not compute latency itself.

A DuckDB container also runs next to MySQL and Connect. The SQL window (`internal/query`) runs the operator's own queries in it and attaches the Tableflow Iceberg REST catalog as `tableflow`, using the `tableflow_api_key`/`tableflow_catalog_endpoint` Terraform outputs, so keep the `duckdb` compose service, its image and those outputs. The freshness charts don't use it.

- `main.go` / `app.go`: Wails entrypoint. `App` methods are bound to the frontend; progress reaches the UI as events (`log`, `step`, `latency`, `sim`, `setup:done`, `teardown:done`). CC credentials are held in memory only.
- `internal/pipeline`: orchestrates the ordered setup steps and teardown, and owns the simulator and latency monitor. Dependencies are injected as `Deps` functions (`wire.go` holds the real ones) so ordering is unit-tested with fakes.
- `internal/terraform`: embedded `tf/*.tf` plus `.terraform.lock.hcl`, run via `terraform-exec`. The binary comes from `hc-install`.
- `internal/compose`: embedded compose project (MySQL 8.4, cp-kafka-connect + Debezium MySQL plugin, DuckDB CLI), rendered into the workspace and driven by the `docker compose` CLI.
- `internal/schema`, `internal/connector`, `internal/sim`, `internal/latency`: MySQL schema, Connect and Schema Registry clients, clinic simulator, Metrics API client and monitor for Tableflow's freshness metrics.
- `internal/query`: the SQL window's backend. Each run is one DuckDB CLI session in the container, fed on stdin: session setup in `.mode trash`, then `.print` of a ready marker (so a setup failure is told from a query failure), then the operator's SQL in `.mode json`. `App.RunQuery` → `Pipeline.Query` → `Deps.RunQuery`; it needs the Terraform outputs of a setup run in this session. The UI is `frontend/src/components/QueryPanel.tsx` with a CodeMirror editor in `SqlEditor.tsx`.
- `frontend/src/App.tsx` lays the UI out as a fixed viewport, with nothing scrolling but the panels: header, `SimulationBar`, then the workspace (`SidePanel`'s rail and drawer for Metrics, Logs and Data flow beside `QueryPanel` with its `SchemaHelper`), then `StatusBar`, whose drawer holds the setup steps. `useResize` drags the drawer widths, the setup panel's height and the editor split, and remembers them in `localStorage`. `frontend/src/tableDocs.ts` is a hand-written mirror of `internal/schema/schema.sql` for the schema helper, so change both together.
- `internal/hostenv`: `main` calls `EnsureToolPath` before anything else. A `.app` started from Finder gets launchd's minimal `PATH`, which lacks Docker Desktop's CLI and credential helpers (`/usr/local/bin`, `Docker.app/Contents/Resources/bin`), so it appends those directories when they exist. Fix the environment there rather than resolving individual binaries: `docker compose` also shells out to `docker-credential-desktop` through `PATH`.
- `internal/config`, `internal/logging`, `internal/retry`: shared foundations. Use `retry.Do` with `retry.Permanent` for non-retryable errors, and log only through slog with sentence-case messages.

## Invariants to preserve

- **Setup ordering**: provision CC (phase 1) → compose up → install schema → register Debezium → start simulator → wait for `<topic>-value` subjects → enable Tableflow (phase 2) → start the Tableflow metrics monitor. Tableflow can't be enabled before the schemas exist, and Debezium only registers a schema with a topic's first record.
- **Topics must be `cleanup.policy=compact`** and every table needs a primary key. Tableflow's native Debezium support (Early Access) requires both. Don't add a Flink unwrap layer.
- **Freshness comes from the Metrics API.** Both metrics are Preview, `MAX`-aggregated gauges that only move when Tableflow commits, and data arrives about three minutes late. `latency.Result` is the whole last 5 hours (`latency.Window`, the most the API allows at one-minute granularity) and replaces the previous one; a failed poll keeps the last good samples and sets `Error`. Don't go back to diffing Iceberg snapshots through DuckDB.
- **Resource names carry a per-operator suffix**: the environment, cluster and service account are named from `pims-<suffix>` so operators sharing one Confluent Cloud account don't collide (environment and service account names are unique per organisation). `config.Workspace.Suffix` creates the suffix once and keeps it in `settings.json`; `terraform.Apply` and `Destroy` pass it as `name_suffix`. Anything that rewrites `settings.json` must load it first and update fields, never replace it, or the suffix is lost and the next apply renames the live resources. Things inside the environment (topics, subjects, the connector) need no suffix.
- **Tableflow is sticky**: once enabled, `terraform.Apply` keeps it on. Changing the AWS region on existing state is refused (`ErrRegionChanged`).
- **Debezium state is scoped to a MySQL "generation"** (`compose.MySQLSecrets.Generation`). Recreating the MySQL volume yields a new connector name and schema-history topic, so stale CC offsets are never reused.
- **Secrets**:
  - CC credentials go to terraform only via process env.
  - Connector configs use `${env:…}` (Connect env config provider).
  - `compose.Exec` takes scripts on stdin (the DuckDB container) and scrubs stderr. `ExecLimit` also caps stdout, and `*ExecError` carries the scrubbed stderr.
  - The Metrics API is called with the operator's Cloud API key, passed from `Setup` to the monitor in memory and never logged. The demo's service account has no Metrics API access, on purpose: `MetricsViewer` is organisation-scoped and would widen it beyond the demo environment.
  - Credential types redact themselves in `String`/`LogValue`/`MarshalJSON`.
- **Query window**: the operator's SQL shares stdin with the setup script, so a line starting with `.` (a DuckDB shell command, such as `.mode` or `.shell`) is rejected before it reaches DuckDB: it would change the output format `internal/query` parses or run programs. DuckDB runs as `bash -c 'set -o pipefail; timeout --signal=KILL … duckdb -batch -bail | head -c N'`. The host can't stop it (stopping the docker client leaves the process running in the container), so the container ends it at the time limit and, through `head`, as soon as it prints past the 8 MiB cap; only 1000 rows are returned. The session sets `memory_limit` and a spill directory the container's user can write. The catalog session sets `httpfs_connection_caching` (off by default, so every request pays a new TCP and TLS handshake: measured 511 s versus 187 s for one scan on a 320 ms link), and the run limit is 5 minutes because that scan takes minutes. One query runs at a time (`ErrQueryBusy`), and queries are refused until setup's services step is done, since Terraform's outputs are known before the container is up. The Tableflow secret is in the session by design, so the SQL is trusted input from the operator. Cells are returned as DuckDB's own text (HUGEINT and BIGINT would lose digits as JavaScript numbers).
- **Timestamps**: `created_at`/`updated_at` are `DATETIME(3)` maintained by MySQL in UTC. Freshness no longer uses them (Tableflow's metrics do), but keep them. Business times (`scheduled_at`, `check_in_at`) come from the simulator's clinic clock.
- **Simulator domain rules are enforced in `internal/sim/model.go`** before writing: one appointment per patient per day, no overlapping provider bookings, visits only for due appointments, one visit per patient at a time. Unique keys in `schema.sql` are only a backstop. The model is mutated only on the run goroutine, under `Simulator.mu`.

## Conventions

- Commit messages follow the `git-commit-message` skill (`.claude/skills/git-commit-message/SKILL.md` at the repository root). Prefix them with `pims: ` and use the Go package name as the scope (`cli` for `main.go`/`app.go`, `frontend` for the UI).
- Review Go changes with the `go-code-reviewer` agent (`.claude/agents/go-code-reviewer.md` at the repository root).
