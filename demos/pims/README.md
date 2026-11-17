# PIMS

Patient Information Management System: a desktop demo of Confluent Cloud
**Tableflow** materialising MySQL tables as Apache Iceberg tables via a
**Debezium** CDC connector, with end-to-end latency charted live.

```
MySQL ──binlog──▶ Debezium (Kafka Connect) ──Avro──▶ Confluent Cloud topics
                                                     (compacted, Schema Registry)
                                                            │ Tableflow
                                                            ▼
                    DuckDB ◀──Iceberg REST catalog── Iceberg tables (managed storage)
```

PIMS is a [Wails](https://wails.io) (Go + React) app that:

1. **Sets up everything**: Confluent Cloud environment, Standard Kafka cluster,
   compacted topics, API keys and Tableflow (Terraform run in-process). It
   also starts MySQL, Kafka Connect with Debezium, and DuckDB (Docker Compose),
   then installs the schema and registers the connector.
2. **Simulates a clinic**: patient registrations, bookings, check-ins,
   walk-ins, no-shows, cancellations and visit completions, at a rate you set.
3. **Charts latency**: the time from a row's MySQL `updated_at` to the
   Iceberg snapshot commit that first contains it, per table.

## Prerequisites

- **Docker** (Docker Desktop on macOS). Nothing else needs to be installed.
  PIMS downloads a pinned, signature-verified Terraform binary on first use.
- A **Confluent Cloud** account and a *Cloud resource management* API key that
  can create environments.
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
2. Click **Set up demo** and follow the steps. First runs take roughly 10–15
   minutes, mostly cluster provisioning and image builds. Setup starts the
   simulator, because Debezium only registers a topic's schema with its first
   record and Tableflow needs that schema.
3. Adjust **events per second**, then watch the latency chart. Tableflow
   commits snapshots periodically, so expect points every few minutes.
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

## How latency is measured

Every 30 seconds PIMS runs the DuckDB CLI in its container. DuckDB attaches the
Tableflow Iceberg REST catalog with the Tableflow API key and, for each table,
reads:

- the newest `updated_at` that has reached Iceberg, and
- the commit time of the earliest Iceberg snapshot at or after it. That
  snapshot is when the S3 data file holding that record was published.

The difference covers the whole path: MySQL write, Debezium capture, Kafka,
Tableflow materialisation and S3 commit. `updated_at` is set by MySQL inside
Docker, so the result depends on the Docker VM clock staying in sync with
the host.

## Project layout

| Path | What it does |
|---|---|
| `main.go`, `app.go` | Wails entrypoint and the methods/events bound to the UI |
| `frontend/` | React + TypeScript UI (Vite) |
| `internal/pipeline` | Orchestrates setup steps, teardown, simulator and latency monitor |
| `internal/terraform` | Embedded Terraform config, run with terraform-exec and hc-install |
| `internal/compose` | Embedded Docker Compose project (MySQL, Connect + Debezium, DuckDB) |
| `internal/schema` | MySQL schema and Debezium user |
| `internal/connector` | Kafka Connect and Schema Registry REST clients |
| `internal/sim` | Clinic simulator: domain model, rate-limited runner, MySQL store |
| `internal/latency` | DuckDB queries against the Tableflow Iceberg catalog |
| `internal/config`, `logging`, `retry` | Credentials and workspace, slog tee to the UI, backoff |

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
