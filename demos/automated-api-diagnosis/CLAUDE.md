# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Automated API diagnosis simulates API gateway logs from 10 APIs into Confluent Cloud, has Flink find the APIs whose error rate turns anomalous, shows their health live, and (optionally) shows AI agent reports on each anomaly. It is a **Launchpad demo**: a Go web service with a Svelte UI, in an ECS task that Launchpad deploys from `tf/`. `README.md` is the overview; `docs/specs/launchpad.md` describes the design; `SPEC.md` is the original brief.

It was converted from `~/Projects/app-monitoring-demo`, a Wails desktop app, and the Wails app was then removed. The Go module is `apidiag`. The Avro namespace `amd.simulator` in `internal/simulator/types.go` is from that project: leave it, since renaming a record is an incompatible schema change.

## Commands

Run these from `demos/automated-api-diagnosis`; this project lives in the `bds-cflt-assets` monorepo.

```sh
go vet ./... && go test -race ./...         # Go unit tests: no cloud account needed
cd frontend && npm ci && npm run build     # the UI, which the server binary embeds
cd tf && terraform init -backend=false && terraform validate && terraform test   # task definition and Flink statements against mock providers
docker build -f docker/Dockerfile -t automated-api-diagnosis .
```

## Architecture

Data path: `simulator → gateway-logs (Avro) → Flink detect_anomalies → api_unhealthy_events → monitor` and, if an agent is deployed, `→ AI agent → api_anomaly_ai_reports → AI reports tab`.

- `cmd/server` is the binary; `internal/server` is its HTTP and SSE layer, access-token gate and `Service`. Terraform provisions everything: the app has no setup step and creates nothing. `Service` runs the simulator and both Kafka readers, keeps in-memory backlogs for tabs opened later, and validates every change to an API's settings.
- `internal/simulator`: the 10 APIs, their event generation and cascade rules, the Kafka producer. `Engine` takes a `Sink` (the producer, or a fake in tests).
- `internal/health`, `internal/reports`: Kafka readers of `api_unhealthy_events` (key and value both Avro) and `api_anomaly_ai_reports`.
- `frontend/`: Svelte. `frontend/src/api.js` is the only module that calls the backend (`fetch` and one `EventSource`); components import from it.
- `tf/`: Confluent Cloud (`confluent.tf`), Flink (`flink.tf`, SQL in `tf/flink/`), AWS (`network.tf`, `cloudfront.tf`, `ecs.tf`, `secrets.tf`). `tf/ai.tf.example` is the optional AI agent.
- `docker/Dockerfile` builds the image, and `.github/workflows/automated-api-diagnosis-image.yml` publishes it. It is a glibc build with cgo, because confluent-kafka-go links librdkafka.

## Invariants to preserve

- **Launchpad's interface**: `tf/` declares `aws_region`, `launchpad_tags`, both Confluent variables and the `launchpad_url` output, and no backend. Launchpad reads only top-level `.tf` files, so `ai.tf.example` is inert until copied.
- **Terraform and the server agree**: the env vars in `tf/ecs.tf` and `internal/server/config.go` must match, `tf/gateway-logs-value.avsc` must equal `simulator.AvroSchemaJSON()`, and topic and table names in Go, `tf/confluent.tf` and `tf/flink/*.sql` must agree. `internal/server/contract_test.go` checks all of it. Change `KongLogEvent` and you must regenerate the `.avsc`.
- **Schema before topic**: Terraform registers `gateway-logs-value` before it creates the topic, so Flink never reads the topic without a schema (a topic Flink has read with no schema stays a single BYTES column). Don't reorder.
- **`api_anomaly_ai_reports` columns are all `NOT NULL`**: `internal/reports` decodes into plain `string`/`int64`, which cannot read a nullable Avro union. `detected_at` is `TIMESTAMP_LTZ(3)`, decoded into an `int64` of milliseconds on purpose (a `time.Time` would be read as microseconds). Same for `health.HealthEvent`, whose key (`api_id`, `detected_at`, `http_status`) and value are decoded separately.
- **The detector is the flip-detection query** (`tf/flink/detect_anomalies.sql`): it emits a row when an API turns unhealthy and one when it recovers (`http_status = 'RECOVERED'`), not one per window. That is why the server keeps backlogs: a late tab would otherwise show every API as healthy. Its LEFT JOIN is required, since recovery windows have no `>= 400` rows. It starts at `earliest-offset` so the detector's 30-window (five minute) baseline includes everything sent so far.
- **Access**: nothing but `/healthz` is served without the access token. People reach the app only through CloudFront (VPC origin to an internal ALB), so `launchpad_url` is HTTPS. State changes need the `X-APIDIAG` header. The server applies only the adjustable fields of an API's settings (rate, error percentages, latency, gateway error, dependencies) and bounds them (`maxRate`, `maxLatencyMs`): names and paths are not the browser's to set, and the cluster is billed.
- **Secrets**: they reach the container only through Secrets Manager. `server.Config` redacts itself in `String`/`LogValue`. The app has no AWS permissions.
- **One task**: `desired_count = 1` with `deployment_maximum_percent = 100`, so there are never two simulators.
- **The AI agent is the user's**: the source never recorded one, so none is deployed. Don't invent a prompt and enable it.

## Conventions

- Commit messages follow the `git-commit-message` skill (`.claude/skills/git-commit-message/SKILL.md` at the repository root). Prefix them with `automated-api-diagnosis: ` and use the Go package name as the scope (`cli` for `main.go`/`app.go`, `frontend` for the UI, `server` for `cmd/server` and `internal/server`, `tf` for the Launchpad Terraform, `docker` for the image). The image workflow is outside the asset, so its commits use `repo: `.
- Review Go changes with the `go-code-reviewer` agent, and everything before a commit with the `security-reviewer` agent (`.claude/agents/` at the repository root).
- Log through slog with sentence-case messages.
