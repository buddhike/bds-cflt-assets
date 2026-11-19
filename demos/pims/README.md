# PIMS

Patient Information Management System: a desktop demo of Confluent Cloud
**Tableflow** materialising MySQL tables as Apache Iceberg tables via a
**Debezium** CDC connector, with Tableflow's freshness metrics charted live.

```
MySQL ──binlog──▶ Debezium (Kafka Connect) ──Avro──▶ Confluent Cloud topics
                                                     (compacted, Schema Registry)
                                                            │ Tableflow
                                                            ▼
                                                     Iceberg tables (managed storage)

PIMS ◀──freshness metrics── Confluent Cloud Metrics API ◀── Tableflow
```

PIMS is a [Wails](https://wails.io) (Go + React) app that:

1. **Sets up everything**: Confluent Cloud environment, Standard Kafka cluster,
   compacted topics, API keys and Tableflow (Terraform run in-process). It
   also starts MySQL, Kafka Connect with Debezium, and a DuckDB container for
   querying the Iceberg tables (Docker Compose), then installs the schema and
   registers the connector. The charts do not use DuckDB; the SQL window does.
2. **Simulates a clinic**: patient registrations, bookings, check-ins,
   walk-ins, no-shows, cancellations and visit completions, at a rate you set.
3. **Charts freshness**: how fresh each Iceberg table is, using Tableflow's own
   `event_freshness_latency_milliseconds` and
   `processing_freshness_latency_milliseconds` metrics, per table.
4. **Runs your SQL**: a query window with DuckDB syntax highlighting that runs
   in the DuckDB container, with the Tableflow tables attached.

## Prerequisites

- **Docker** (Docker Desktop on macOS). Nothing else needs to be installed.
  PIMS downloads a pinned, signature-verified Terraform binary on first use.
- A **Confluent Cloud** account and a *Cloud resource management* API key that
  can create environments. PIMS also uses this key to read Tableflow's metrics
  from the Metrics API, so its owner must be allowed to do that too (for
  example with the `MetricsViewer` role).
- **Tableflow native Debezium support is in Early Access.** Your organisation
  or environment must be enrolled, or the Tableflow tables will not
  materialise.

> **Cost:** PIMS creates billable Confluent Cloud resources: a Standard
> cluster, Tableflow and Schema Registry. Use **Tear down** when you are
> done.

## Running

```sh
wails dev          # live-reload development build
wails build        # produces build/bin/pims.app (macOS)
```

1. Enter the AWS region and your Confluent Cloud API key and secret. The key
   and secret are kept in memory for the session only.
2. Click **Set up demo** and follow the steps in the status bar along the
   bottom of the window; click it to see each step. First runs take roughly
   10–15 minutes, mostly cluster provisioning and image builds. Setup starts
   the simulator, because Debezium only registers a topic's schema with its
   first record and Tableflow needs that schema.
3. Adjust **events per second** in the simulation bar under the header, then
   open **Metrics** on the left to watch the freshness charts. Tableflow
   commits periodically and the Metrics API is a few minutes behind, so expect
   the first values several minutes after Tableflow is enabled.
4. Click **Tear down** to delete the connector, local containers, the MySQL
   volume and every Confluent Cloud resource PIMS created.

Re-running setup is safe and resumes from whatever already exists.
Changing the AWS region requires a teardown first.

Working files live in the OS config dir, e.g.
`~/Library/Application Support/pims`:

- Terraform state, which contains the generated API keys
- the rendered compose project
- MySQL passwords

Everything there is user-only (0700/0600).

## Finding your way around

- **Header**: set up or tear down the demo.
- **Simulation bar**, just below it: the rate, **Start** and **Stop**. Once the
  simulator has run it also shows the clinic clock and the counts, and
  **Breakdown** adds the count of each kind of event.
- **Rail on the left**: **Metrics**, **Logs** and **Data flow** each draw a
  panel out beside the SQL window. Click the highlighted button again to close
  it, and drag the panel's edge to resize it; each section remembers its width.
  A badge on **Logs** counts errors, or warnings when there are no errors.
- **SQL window**: the rest of the screen. **Tables** shows or hides the schema
  helper beside the editor, and the line between the editor and the results
  drags to give either more room.
- **Status bar**, along the bottom: where setup is, with the AWS region and the
  number of errors and warnings. Click it to draw out the setup steps, and drag
  the top edge to resize them.

## How freshness is shown

The **Data flow** section of the left-hand panel shows this path as a diagram,
with the same explanation, and its arrows animate while the simulator runs.

PIMS does not measure latency itself. Tableflow reports two gauges per table
to the Confluent Cloud Metrics API, and PIMS charts them:

| Metric | What it is |
|---|---|
| `io.confluent.tableflow/event_freshness_latency_milliseconds` | The age of the newest record Tableflow has written to the table: from the record's timestamp to when Tableflow wrote it. It includes everything before Tableflow (the MySQL write, Debezium capture and Kafka). |
| `io.confluent.tableflow/processing_freshness_latency_milliseconds` | Tableflow's own processing time: from picking a record up to writing it to the table. |

Every 60 seconds PIMS sends two queries to
`POST https://api.telemetry.confluent.cloud/v2/metrics/cloud/query`, one per
metric, filtered to the demo's Kafka cluster (`resource.kafka.id`) and grouped
by `metric.topic`. Each asks for the last 5 hours at one-minute granularity
(`PT1M`), which is the longest window the API allows at that granularity, so a
poll returns up to 360 values per table per metric. The result replaces the
previous one, so the charts always show the last 5 hours, and a poll that
fails keeps the last good reading on screen and shows the error above it. A
failed poll is not retried within the minute: the next one reads the whole
window again, so nothing is lost. PIMS keeps nothing older, so anything beyond
5 hours would need a coarser granularity or PIMS storing its own history.

Each poll logs a `Queried Tableflow freshness metrics` line in the **Logs** section
with how many samples came back for each metric, the time of the newest, and
any topics in the answer that PIMS does not chart (`other_topics`). If a chart
stays empty, that line tells apart "the API returned nothing for this window"
from "it returned rows PIMS could not match to a table".

Things to know when reading the charts:

- Both metrics are in **Preview** and can change before general availability.
- Tableflow updates the gauges when it commits to a table, so each table's
  value moves in steps and repeats between commits. Each point is the highest
  value Tableflow reported for that table in that minute.
- The Metrics API makes data available about three minutes after it is
  produced, and the first values can take several minutes to appear after
  Tableflow is enabled. Until then the charts say they are waiting.
- The query uses the Cloud API key you entered, over HTTPS. The Metrics API
  rejects keys scoped to a Kafka cluster, and a `401` or `403` is shown in the
  panel with a hint. PIMS creates no extra key for this, so the demo's service
  account keeps its environment-only scope.
- The charts do not read the Iceberg tables. The SQL window does.

## Querying the Tableflow tables

The **SQL** window runs your own DuckDB SQL in the `duckdb` container. Once
Tableflow is enabled, PIMS attaches its Iceberg REST catalog as `tableflow`, so
the tables are `tableflow."<kafka cluster id>".patients`, `.appointments` and
`.visits`. The window shows the exact names and starts you off with a query for
one of them. Press `⌘↵` (`Ctrl+Enter` elsewhere) or **Run**; with text
selected, only the selection runs. `DESCRIBE` shows a table's columns.

The **Tables** helper beside the editor lists each table's columns with their
types, keys and allowed values. Click a table's **Insert** button or a column
name to put it in the query at the cursor. It describes the MySQL tables, so
use `DESCRIBE` for the Iceberg tables' own types.

Things to know:

- Each run is a **fresh DuckDB session**. The catalog is attached again, which
  takes a couple of seconds, and nothing a run creates (tables, settings) is
  there for the next one.
- You can run several statements at once. The result of the last one that
  returned rows is shown, and the run stops at the first error.
- A result shows its first 1,000 rows and says how many there were in all.
  Output over 8 MiB is refused: add a `LIMIT` or select fewer columns.
- A run is cut off after 5 minutes, inside the container too, so a runaway
  query does not keep using it. DuckDB may use 2 GB of memory and spills to
  disk beyond that. One query runs at a time.
- **Reading a Tableflow table is slow on a high-latency network.** DuckDB reads
  every manifest of the table before any data, even under a `LIMIT`, and each
  file is a separate request to S3. A table with ~130 manifests and ~390 data
  files took 3 minutes over a 320 ms link (about 1 second per new connection
  without connection reuse, which PIMS turns on) and is a few seconds on a fast
  one. The button shows the elapsed time while it runs. `DESCRIBE` only reads
  the table's metadata and takes seconds.
- Only SQL is accepted. A line starting with `.`, which the DuckDB shell takes
  as one of its own commands (`.mode`, `.shell`), is rejected.
- The session has the demo's Tableflow API key loaded to read the catalog, so
  do not run SQL you do not trust.
- The Tableflow credentials come from the setup run, so after relaunching PIMS
  run setup again before querying. It resumes where things are.

## Project layout

| Path | What it does |
|---|---|
| `main.go`, `app.go` | Wails entrypoint and the methods/events bound to the UI |
| `frontend/` | React + TypeScript UI (Vite) |
| `internal/pipeline` | Orchestrates setup steps, teardown, simulator and metrics monitor |
| `internal/terraform` | Embedded Terraform config, run with terraform-exec and hc-install |
| `internal/compose` | Embedded Docker Compose project (MySQL, Connect + Debezium, DuckDB) |
| `internal/schema` | MySQL schema and Debezium user |
| `internal/connector` | Kafka Connect and Schema Registry REST clients |
| `internal/sim` | Clinic simulator: domain model, rate-limited runner, MySQL store |
| `internal/latency` | Metrics API client and monitor for Tableflow's freshness metrics |
| `internal/query` | Runs the SQL window's queries in the DuckDB container and parses the results |
| `internal/config`, `logging`, `retry` | Credentials and workspace, slog tee to the UI, backoff |

## Releasing

`release.md` holds the version of the latest release and nothing else. Bump it
on `main` and the repository's release workflow builds the app for macOS
(universal), Windows and Linux, then publishes the GitHub release `pims-v<version>`
with those binaries and a `SHA256SUMS` file. The builds are not signed with a
paid certificate, so macOS and Windows ask for approval on first launch. The
release notes say how.

## Tests

```sh
go test -race ./...                                  # unit tests
go test -run TestSimulationInvariants ./internal/sim # a single test

# Opt-in integration tests
PIMS_INTEGRATION=1 go test ./internal/terraform      # terraform validate with the real provider
docker run -d --name pims-test-mysql -e MYSQL_ROOT_PASSWORD=testroot -e MYSQL_DATABASE=pims \
  -p 127.0.0.1:33307:3306 mysql:8.4 --gtid-mode=ON --enforce-gtid-consistency=ON --default-time-zone=+00:00
PIMS_TEST_MYSQL_ROOT_PASSWORD=testroot PIMS_TEST_MYSQL_PORT=33307 go test ./internal/schema ./internal/sim

cd frontend && npx tsc --noEmit                      # type-check the UI
```

The compose and DuckDB tests use the local Docker daemon when it is available
and skip otherwise.
