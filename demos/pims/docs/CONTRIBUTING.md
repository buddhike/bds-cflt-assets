# Contributing

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
