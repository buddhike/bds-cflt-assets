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
udf/emit-after-gap/build.sh                # test the Flink function and build its jar (Docker)
```

## Architecture

Data path: `simulator → gateway-logs (Avro) → Flink detect_anomalies → api_unhealthy_events → monitor` and `→ api_anomalies → EmitAfterGap → AI agent (Bedrock) → api_anomaly_ai_reports → AI reports tab`.

- `cmd/server` is the binary; `internal/server` is its HTTP and SSE layer, access-token gate and `Service`. Terraform provisions everything: the app has no setup step and creates nothing. `Service` runs the simulator and both Kafka readers, keeps in-memory backlogs for tabs opened later, and validates every change to an API's settings.
- `internal/simulator`: the 10 APIs, their event generation and cascade rules, the Kafka producer. `Engine` takes a `Sink` (the producer, or a fake in tests).
- `internal/health`, `internal/reports`: Kafka readers of `api_unhealthy_events` (key and value both Avro) and `api_anomaly_ai_reports`.
- `frontend/`: Svelte. `frontend/src/api.js` is the only module that calls the backend (`fetch` and one `EventSource`); components import from it.
- **Look and feel**: `frontend/src/graphite.css` is a copy of `libs/design/graphite.css`, the shared design system of Launchpad and every demo (spec: `demos/launchpad/docs/specs/002_visual_design.md`). Never edit the copy: change the canonical file and run `libs/design/sync.sh`. Components keep layout in their `<style>` blocks and set no colour, font or radius of their own: use the tokens (`var(--surface)`, `var(--danger)`…) and the shared classes (`.card`, `.health`, `.pill`, `.notice`, `.topbar`). The body is `dense`; `Logo.svelte` and `Notice.svelte` are the shared mark and notice.
- `tf/`: Confluent Cloud (`confluent.tf`), Flink (`flink.tf`, SQL in `tf/flink/`), AWS (`network.tf`, `cloudfront.tf`, `ecs.tf`, `secrets.tf`). `tf/ai.tf` is the Bedrock AI agent (IAM user, Flink connection, model, agent, the `EmitAfterGap` function). `udf/emit-after-gap/` is the Maven project of that function.
- `docker/Dockerfile` builds the image, and `.github/workflows/automated-api-diagnosis-image.yml` publishes it. It is a glibc build with cgo, because confluent-kafka-go links librdkafka.

## Invariants to preserve

- **Launchpad's interface**: `tf/` declares `aws_region`, `launchpad_tags`, both Confluent variables and the `launchpad_url` output, and no backend. Launchpad reads only top-level `.tf` files, so keep these in top-level files (`ai.tf` is one).
- **Terraform and the server agree**: the env vars in `tf/ecs.tf` and `internal/server/config.go` must match, `tf/gateway-logs-value.avsc` must equal `simulator.AvroSchemaJSON()`, and topic and table names in Go, `tf/confluent.tf` and `tf/flink/*.sql` must agree. `internal/server/contract_test.go` checks all of it. Change `KongLogEvent` and you must regenerate the `.avsc`.
- **Schema before topic**: Terraform registers `gateway-logs-value` before it creates the topic, so Flink never reads the topic without a schema (a topic Flink has read with no schema stays a single BYTES column). Don't reorder.
- **`api_anomaly_ai_reports` columns are all `NOT NULL`**: `internal/reports` decodes into plain `string`/`int64`, which cannot read a nullable Avro union. `detected_at` is `TIMESTAMP_LTZ(3)`, decoded into an `int64` of milliseconds on purpose (a `time.Time` would be read as microseconds). Same for `health.HealthEvent`, whose key (`api_id`, `detected_at`, `http_status`) and value are decoded separately.
- **The detector emits every window** (`tf/flink/detect_anomalies.sql`): a row per API and failing HTTP status per 10-second window, or one `http_status = 'RECOVERED'` row when nothing failed, with `is_anomaly` saying whether the window was out of bounds. The monitor shows the latest window per API, and the server keeps the last 500 rows for tabs opened later. Its LEFT JOIN is required, since healthy windows have no `>= 400` rows. It starts at `earliest-offset` so the detector's 30-window (five minute) baseline includes everything sent so far.
- **Statement text is frozen on a running demo**: an update replaces a Flink statement whose text changed, by deleting it and running the new one. A plain `CREATE TABLE`, `CREATE MODEL`, `CREATE AGENT` or `CREATE FUNCTION` then fails because the object exists, and so does the update. Table statements use `IF NOT EXISTS` (`contract_test.go` checks); for the others, change the text only with a new object name, and expect a change to fail an update of a demo that is already running.
- **Access**: nothing but `/healthz` is served without the access token. People reach the app only through CloudFront (VPC origin to an internal ALB), so `launchpad_url` is HTTPS. State changes need the `X-APIDIAG` header. The server applies only the adjustable fields of an API's settings (rate, error percentages, latency, gateway error, dependencies) and bounds them (`maxRate`, `maxLatencyMs`): names and paths are not the browser's to set, and the cluster is billed.
- **Secrets**: they reach the container only through Secrets Manager. `server.Config` redacts itself in `String`/`LogValue`. The app has no AWS permissions.
- **One task**: `desired_count = 1` with `deployment_maximum_percent = 100`, so there are never two simulators.
- **The agent's Bedrock credential is made by Terraform and goes nowhere else**: `aws_iam_access_key.flink_llm` → `confluent_flink_connection.bedrock`, the key only allowed to invoke the one model. No literal key in the tree, no output, no Flink statement or Secrets Manager entry carrying it (`contract_test.go` checks). It is in Terraform's state, like the demo's other secrets. The prompt in `tf/flink/ai_agent.sql` is the user's: don't reword it.
- **`EmitAfterGap` is a release, not a committed file.** Launchpad's runner has no Java. `.github/workflows/automated-api-diagnosis-udf.yml` builds `udf/emit-after-gap/` on a push and publishes the jar as the release `emit-after-gap-<hash>`, where `<hash>` is of `pom.xml`, `build.sh` and `src/main/**` from `tf/modules/udf-release`. `tf/ai.tf` runs the same module, so Terraform asks for exactly that release; keep the workflow and `ai.tf` on the one module, and never edit or delete a release. A launch before the workflow finishes fails with a 404 message: launch again. A new jar reaches a running demo only after teardown and a new launch. `ai_reports` reads `api_anomalies`, a table (not a view: a process table function takes a table), made by `create_anomalies.sql`.

## Conventions

- Commit messages follow the `git-commit-message` skill (`.claude/skills/git-commit-message/SKILL.md` at the repository root). Prefix them with `automated-api-diagnosis: ` and use the Go package name as the scope (`cli` for `main.go`/`app.go`, `frontend` for the UI, `server` for `cmd/server` and `internal/server`, `tf` for the Launchpad Terraform, `docker` for the image). The image workflow is outside the asset, so its commits use `repo: `.
- Review Go changes with the `go-code-reviewer` agent, and everything before a commit with the `security-reviewer` agent (`.claude/agents/` at the repository root).
- Log through slog with sentence-case messages.
