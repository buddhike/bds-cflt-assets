# Contributing

## Project layout

| Path | What it does |
|---|---|
| `cmd/server`, `internal/server` | The Launchpad server: HTTP API, SSE events, access gate and wiring of the pipeline |
| `internal/tableflow`, `internal/duckdbagent` | Confluent Cloud Tableflow API client, and the agent that runs DuckDB in its container |
| `tf/`, `docker/` | The Terraform Launchpad deploys, and the server and DuckDB images |
| `frontend/` | React + TypeScript UI (Vite) |
| `internal/pipeline` | Orchestrates the setup steps, and owns the simulator and metrics monitor |
| `internal/terraform` | The `Outputs` type: what Launchpad's `tf/` provisioned, as the pipeline reads it |
| `internal/compose` | The Kafka Connect image source, and the MySQL secrets and exec error types the pipeline and query runner share |
| `internal/schema` | MySQL schema and Debezium user |
| `internal/connector` | Kafka Connect and Schema Registry REST clients |
| `internal/sim` | Clinic simulator: domain model, rate-limited runner, MySQL store |
| `internal/latency` | Metrics API client and monitor for Tableflow's freshness metrics |
| `internal/query` | Runs the SQL window's queries in the DuckDB container and parses the results |
| `internal/config`, `logging`, `retry` | Credentials, slog tee to the UI, backoff |

## Releasing

PIMS is not released as a binary. `.github/workflows/pims-images.yml` publishes
the container images on every push, and Launchpad deploys the images of the
commit it downloaded.

## Tests

```sh
go test -race ./...                                  # unit tests
go test -run TestSimulationInvariants ./internal/sim # a single test

# Opt-in integration tests
docker run -d --name pims-test-mysql -e MYSQL_ROOT_PASSWORD=testroot -e MYSQL_DATABASE=pims \
  -p 127.0.0.1:33307:3306 mysql:8.4 --gtid-mode=ON --enforce-gtid-consistency=ON --default-time-zone=+00:00
PIMS_TEST_MYSQL_ROOT_PASSWORD=testroot PIMS_TEST_MYSQL_PORT=33307 go test ./internal/schema ./internal/sim

cd frontend && npx tsc --noEmit                      # type-check the UI
```

The DuckDB tests use the local Docker daemon and the `pims-duckdb` image when they
are available and skip otherwise.
