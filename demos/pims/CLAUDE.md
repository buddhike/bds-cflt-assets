# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

PIMS (Patient Information Management System) is a demo that showcases Confluent Cloud (CC) **Tableflow** with a **Debezium** CDC connector, materializing MySQL tables (patients, visits, appointments) as Iceberg tables. It runs only as a **Launchpad demo**: a Go + React/TypeScript web service in an ECS task that Launchpad deploys from `tf/`. It:

1. Fully automates setup of every infrastructure component.
2. Simulates realistic patient registrations, visits and appointments.
3. Shows how fresh the Iceberg tables are, charting Tableflow's own freshness metrics.

`docs/specs/design.md` and `docs/specs/launchpad.md` are the source of truth for intended behavior. `README.md` has the overview.

## Commands

Run these from `demos/pims`; this project lives in the `bds-cflt-assets` monorepo.

```sh
go vet ./... && go test -race ./...          # all Go unit tests
go test -run TestSimulationInvariants ./internal/sim   # a single Go test
cd frontend && npx tsc --noEmit             # type-check the UI
cd frontend && npm run build               # build the UI that the server embeds
```

Opt-in integration tests (skipped by default):
- `PIMS_TEST_MYSQL_ROOT_PASSWORD=… PIMS_TEST_MYSQL_PORT=… go test ./internal/schema ./internal/sim` run against a real MySQL 8.4 (see `docs/CONTRIBUTING.md` for the `docker run` line).
- The DuckDB tests in `internal/query` run automatically when Docker and the `pims-duckdb` image are available.

## Architecture

Data path: `MySQL → Debezium (Kafka Connect, local) → CC Kafka topics (Avro + Schema Registry) → Tableflow (Iceberg, managed storage)`

Freshness path: `Tableflow → CC Metrics API → PIMS`. PIMS polls `io.confluent.tableflow/event_freshness_latency_milliseconds` and `…/processing_freshness_latency_milliseconds` once a minute and charts them. It does not compute latency itself.

A DuckDB container also runs next to MySQL and Connect in the ECS task. The SQL window (`internal/query`) runs the operator's own queries in it and attaches the Tableflow Iceberg REST catalog as `tableflow`, using the `tableflow_api_key`/`tableflow_catalog_endpoint` Terraform outputs, so keep the `duckdb` container, its image and those outputs. The freshness charts don't use it.

- `cmd/server` / `internal/server`: the entrypoint. Progress reaches the UI as SSE events (`log`, `step`, `latency`, `sim`, `setup:done`); the UI calls `/api/*`. CC credentials come from the task's environment.
- `internal/pipeline`: orchestrates the ordered setup steps and owns the simulator and latency monitor. Dependencies are injected as `Deps` functions (`internal/server/deps.go` holds the real ones) so ordering is unit-tested with fakes. It has no teardown: Launchpad owns the demo's life.
- `internal/terraform`: only the `Outputs` type (what `tf/` provisioned, read from the task's environment) and `ApplyOptions`. It runs no Terraform.
- `internal/compose`: only `MySQLSecrets`, `ExecError` and `ErrOutputTooLarge`, shared by the pipeline, `internal/query` and `internal/duckdbagent`, plus `project/connect/Dockerfile`, the Kafka Connect image the workflow builds. It runs no Compose.
- `internal/schema`, `internal/connector`, `internal/sim`, `internal/latency`: MySQL schema, Connect and Schema Registry clients, clinic simulator, Metrics API client and monitor for Tableflow's freshness metrics.
- `internal/query`: the SQL window's backend. Each run is one DuckDB CLI session in the container, fed on stdin: session setup in `.mode trash`, then `.print` of a ready marker (so a setup failure is told from a query failure), then the operator's SQL in `.mode json`. `Service.RunQuery` → `Pipeline.Query` → `Deps.RunQuery`; it needs the outputs of a setup run in this session. The UI is `frontend/src/components/QueryPanel.tsx` with a CodeMirror editor in `SqlEditor.tsx`.
- `frontend/src/App.tsx` lays the UI out as a fixed viewport, with nothing scrolling but the panels: header, `SimulationBar`, then the workspace (`SidePanel`'s rail and drawer for Metrics, Logs and Data flow beside `QueryPanel` with its `SchemaHelper`), then `StatusBar`, whose drawer holds the setup steps. `useResize` drags the drawer widths, the setup panel's height and the editor split, and remembers them in `localStorage`. `frontend/src/tableDocs.ts` is a hand-written mirror of `internal/schema/schema.sql` for the schema helper, so change both together.
- **Look and feel**: `frontend/src/graphite.css` is a copy of `libs/design/graphite.css`, the shared design system of Launchpad and every demo (spec: `demos/launchpad/docs/specs/002_visual_design.md`). Never edit the copy: change the canonical file and run `libs/design/sync.sh`. `style.css` holds only PIMS's layout and the components only it has (rail drawers, schema helper, data-flow diagram), and sets no colour: use the tokens, including in the CodeMirror theme and SVG. The body is `dense`. The top bar, pills, notices, steps, KPI tiles and status bar are the shared components.
- **Launchpad** (`docs/specs/launchpad.md`): the pipeline, simulator, UI and charts run as a web service in an ECS task that Launchpad deploys from `tf/`. `cmd/server` is the binary (`serve`, or `duckdb-agent` as the DuckDB container's entry point); `internal/server` is its HTTP and SSE layer, its access-token gate and its wiring of `pipeline.Deps` (no Docker or Terraform step: it waits for the sibling containers, enables Tableflow itself through the Confluent Cloud API once the schemas exist, and disables it again on `POST /shutdown`); `internal/tableflow` is that API client; `internal/duckdbagent` is `docker exec` for DuckDB over loopback HTTP, with `query.Runner` unchanged. `frontend/src/api.ts` is the UI's HTTP and SSE client; the UI has no setup or teardown buttons beyond a retry. `docker/Dockerfile` builds the server and DuckDB images, the workflow `.github/workflows/pims-images.yml` publishes them, and the Connect image is built from `internal/compose/project/connect` (the workflow names that path, so it stays until the workflow moves it).
- `internal/config` (the `Credentials` type, which redacts itself), `internal/logging`, `internal/retry`: shared foundations. Use `retry.Do` with `retry.Permanent` for non-retryable errors, and log only through slog with sentence-case messages.

Checks:

```sh
go test -race ./internal/server ./internal/duckdbagent ./cmd/...
cd tf && terraform init -backend=false && terraform test   # renders the task definition against mock providers
docker build -f docker/Dockerfile --target server -t pims-server .   # and --target duckdb
```

## Invariants to preserve

- **Setup ordering**: provision CC (phase 1, Terraform run by Launchpad) → wait for the sibling containers → install schema → register Debezium → start simulator → wait for `<topic>-value` subjects → enable Tableflow (phase 2) → start the Tableflow metrics monitor. Tableflow can't be enabled before the schemas exist, and Debezium only registers a schema with a topic's first record.
- **Topics must be `cleanup.policy=compact`** and every table needs a primary key. Tableflow's native Debezium support (Early Access) requires both. Don't add a Flink unwrap layer.
- **Freshness comes from the Metrics API.** Both metrics are Preview, `MAX`-aggregated gauges that only move when Tableflow commits, and data arrives about three minutes late. `latency.Result` is the whole last 5 hours (`latency.Window`, the most the API allows at one-minute granularity) and replaces the previous one; a failed poll keeps the last good samples and sets `Error`. Don't go back to diffing Iceberg snapshots through DuckDB.
- **Resource names carry a per-demo suffix**: the environment, cluster and service account are named `<launchpad_demo_id>-<suffix>` (`random_id.suffix` in `tf/confluent.tf`) so operators sharing one Confluent Cloud account don't collide (environment and service account names are unique per organisation). Changing the `random_id` or the name expression renames the live resources on the next apply. Things inside the environment (topics, subjects, the connector) need no suffix.
- **Tableflow is enabled by the server, not Terraform**: `Provision` with `EnableTableflow` calls the Tableflow API, and `POST /shutdown` is the only thing that turns it off.
- **Debezium state is scoped to a MySQL "generation"** (`compose.MySQLSecrets.Generation`, the ECS task id). A new task has a new MySQL volume and so a new connector name and schema-history topic, so stale CC offsets are never reused.
- **Secrets**:
  - The Cloud API key reaches the server through the task's environment (Secrets Manager); nothing writes it to disk.
  - Connector configs use `${env:…}` (Connect env config provider).
  - `duckdbagent.Client.ExecLimit` takes scripts on stdin (the DuckDB container), scrubs stderr and caps stdout; `*compose.ExecError` carries the scrubbed stderr.
  - The Metrics API is called with the operator's Cloud API key, passed from `Setup` to the monitor in memory and never logged. The demo's service account has no Metrics API access, on purpose: `MetricsViewer` is organisation-scoped and would widen it beyond the demo environment.
  - Credential types redact themselves in `String`/`LogValue`/`MarshalJSON`.
- **Launchpad**: the env vars in `tf/ecs.tf` and `internal/server/config.go` must match, which `contract_test.go` checks. Nothing but `/healthz` and `POST /shutdown` (bearer token) is served without the access token. People reach the app only through CloudFront (`tf/cloudfront.tf`, VPC origin to an internal ALB), so `launchpad_url` is HTTPS. Debezium's generation is the ECS task id, because MySQL's data lives and dies with its task. The server has no code that deletes resources (no teardown in `pipeline`, no `Destroy` dep): Launchpad owns the demo's life. The one thing the app undoes is Tableflow, which it switched on: `POST /shutdown` (bearer access token, not the cookie) disables it. Launchpad calls it through the `launchpad_shutdown` output before `terraform destroy`, over HTTPS. It answers 500, never 502, when it tried and failed, because Launchpad destroys the demo anyway on 502, 503, 504 or no answer. Terraform must not manage `confluent_tableflow_topic` (`contract_test.go` checks). The DuckDB agent runs only the exact pipeline `query.Runner` builds; if `duckdbCommand` changes, change the allow-list in `duckdbagent` with it.
- **Query window**: the operator's SQL shares stdin with the setup script, so a line starting with `.` (a DuckDB shell command, such as `.mode` or `.shell`) is rejected before it reaches DuckDB: it would change the output format `internal/query` parses or run programs. DuckDB runs as `bash -c 'set -o pipefail; timeout --signal=KILL … duckdb -batch -bail | head -c N'`. The host can't stop it (stopping the docker client leaves the process running in the container), so the container ends it at the time limit and, through `head`, as soon as it prints past the 8 MiB cap; only 1000 rows are returned. The session sets `memory_limit` and a spill directory the container's user can write. The catalog session sets `httpfs_connection_caching` (off by default, so every request pays a new TCP and TLS handshake: measured 511 s versus 187 s for one scan on a 320 ms link), and the run limit is 5 minutes because that scan takes minutes. One query runs at a time (`ErrQueryBusy`), and queries are refused until setup's services step is done, since Terraform's outputs are known before the container is up. The Tableflow secret is in the session by design, so the SQL is trusted input from the operator. Cells are returned as DuckDB's own text (HUGEINT and BIGINT would lose digits as JavaScript numbers).
- **Timestamps**: `created_at`/`updated_at` are `DATETIME(3)` maintained by MySQL in UTC. Freshness no longer uses them (Tableflow's metrics do), but keep them. Business times (`scheduled_at`, `check_in_at`) come from the simulator's clinic clock.
- **Simulator domain rules are enforced in `internal/sim/model.go`** before writing: one appointment per patient per day, no overlapping provider bookings, visits only for due appointments, one visit per patient at a time. Unique keys in `schema.sql` are only a backstop. The model is mutated only on the run goroutine, under `Simulator.mu`.

## Conventions

- Commit messages follow the `git-commit-message` skill (`.claude/skills/git-commit-message/SKILL.md` at the repository root). Prefix them with `pims: ` and use the Go package name as the scope (`frontend` for the UI, `server` for `cmd/server` and `internal/server`, `tf` for the Launchpad Terraform, `docker` for the images). The image workflow is outside the asset, so its commits use `repo: `.
- Review Go changes with the `go-code-reviewer` agent (`.claude/agents/go-code-reviewer.md` at the repository root).
