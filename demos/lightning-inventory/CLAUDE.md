# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Lightning inventory shows Confluent Cloud Lightning Tables on a retail scenario. Producers send real-time inventory positions (`sku`, `location_id`, `quantity`; quantity is the latest at that location) to a Kafka topic keyed by `sku|location_id`, with a Lightning Table on the topic, made by Terraform (`confluent_rtce_topic`). A query load generator issues two scenarios against the table (`by_sku`: all locations for one SKU; `by_locations`: one SKU at 1..2000 locations), and the UI charts p50/p95/p99 latency per second for each from CloudWatch. The operator sets the rates: events/sec (default 10, max 200) and queries/sec (default 50, max 1000), and clicks Start/Stop. It is a **Launchpad demo**: a Go service in three ECS services that Launchpad deploys from `tf/`. `README.md` is the overview; `docs/specs/launchpad.md` is the design, with the Confluent documentation facts and what is *not* documented.

Go module `lightninginv`. **Not yet run in the cloud**: the first launch checklist is at the end of the design doc.

## Commands

Run from `demos/lightning-inventory`.

```sh
go vet ./... && go test -race ./...          # no cloud account needed (the worker tests take ~30 s: they run real 1 s polls and pacers)
cd frontend && npm ci && npm run build       # the UI, which the server binary embeds
cd tf && terraform init -backend=false && terraform validate && terraform fmt -check -recursive && terraform test
docker build -f docker/Dockerfile -t lightning-inventory .   # CI only here: no Docker locally
../../libs/design/sync.sh --check && node ../../libs/design/contrast.mjs
```

## Architecture

One image, one binary, `LIGHTNING_ROLE` = `control` | `producer` | `query`; three ECS services.

- `cmd/server`: wiring per role. `internal/config`: env per role (a role reads, and is given, only what it uses).
- `internal/server` (control): UI + API (`/api/*`), the access gate, `/internal/heartbeat` (workers, worker token, refused if it came via CloudFront), a recover middleware, strict JSON (unknown fields and trailing data refused; the heartbeat ignores unknown fields only, for rolling deploys), the read-only table status poller (`background.go`: GET only, backs off on errors, 429 and 4xx) and in-memory state. `/api/latency` coalesces CloudWatch reads (one in flight per range, 5 s cache, 10 s timeout). It splits each rate between the workers that polled in the last 5 s and leases the 64 catalogue-load shards to producers.
- `internal/worker`: `Control` (poll once a second, plan goes stale after 3 s and a poll times out after 1 s, so `StaleAfter + PollTimeout` < control's 5 s `aliveFor` and a cut-off worker stops before its share is reassigned (the server test models it); counts of failed polls are kept and merged into the next one), `Pace` (open-loop pacer; credit capped at rate+1 so rates under 1/s fire), `Producer` (live events + catalogue load, franz-go + Avro in the Confluent wire format; a shard has its own error counter, never zeroed by the heartbeat; the shard's records carry a context cancelled only to abandon the shard, never to end the pacer; a catalogue-load **epoch** in the plan makes a second load start from scratch), `Querier` (load generator, 512 in flight per task, per-second histograms, a bounded backlog of seconds CloudWatch refused, failure outcomes logged every 10 s).
- `internal/catalog`: the population as a pure function (1M SKUs x 2000 locations, each SKU at 1..2n-1 locations). **Do not change it on a running demo**: the keys already loaded would no longer match (`catalog_test.go` pins values).
- `internal/lightning`: query endpoint client and the rtce-topics client, **read-only** (status), the one place the query SQL is written (`queries.go`).
- `internal/cw`: latency histogram (5 % log buckets, <= 150 values), `Recorder`, `Publisher` (PutMetricData, `StorageResolution=1`, Values/Counts; 5 s per call, whole seconds per call, returns what it could not send), `Reader` (GetMetricData p50/p95/p99/SampleCount at Period 1).
- `internal/confluentmetrics`: the Metrics API (`io.confluent.rtce/...`), shown apart from the latency charts.
- `internal/protocol`: heartbeat/plan types. `internal/contract`: the cross-language tests.
- `frontend/`: Svelte 5. `src/api.js` is the only module that calls the backend. `src/graphite.css` is a copy of `libs/design/graphite.css`: never edit it.
- `tf/`: Confluent (`confluent.tf`), AWS (`network.tf`, `cloudfront.tf`, `secrets.tf`, `ecs.tf`), `inventory-value.avsc`.

## Invariants to preserve

- **Launchpad's interface**: `tf/` declares `aws_region`, `launchpad_tags`, both Confluent variables, the `launchpad_url` output, no backend, in top-level `.tf` files.
- **Terraform owns the Lightning Table and everything else; there is no shutdown hook** (decision of the owner, 2026-11-27, replacing the earlier "the app enables it"): `confluent_rtce_topic` in `tf/confluent.tf`, and the producer and query services depend on it so destroy stops them first, then the table, a 60 s `time_sleep`, then the topic. No `launchpad_shutdown` output, no `/shutdown`, no Enable/Disable in `internal/lightning` (the client only GETs the status). `contract_test.go` fails otherwise. Nothing the app creates may block `terraform destroy`: it creates no resource. The control task still polls the status and the UI gates loads on ACTIVE, because Terraform may return before it.
- **Partition key is `sku|location_id`** (raw string key, one per pair), 6 partitions, `cleanup.policy=compact` (the table's upsert mode). Value schema `lightning.inventory.InventoryPosition` (`sku`, `location_id`, `quantity`, all required); `tf/inventory-value.avsc` must equal `worker.SchemaJSON`. Topic name has no hyphen (the rtce API's pattern). Changing `partitions` on a running demo replaces the topic.
- **Rates are bounded by the server code** (a heartbeat's counts too, and unknown JSON fields are refused), never the browser: events 1..200 (default 10), queries 1..1000 (default 50), seed 25,000 records/s total (one-off, fixed), 1..2000 locations per query, 512 in flight per task. The browser sets only `running` and `rate`. Nothing autostarts. A query start needs the table ACTIVE; the catalogue load needs it too (backfill on enable is undocumented).
- **Latency is client-side** (request sent to body read, HTTP 200 only); 429/other/202 are `Errors`, a full in-flight cap is `Dropped`. CloudWatch computes percentiles over all tasks' pooled samples: do not publish per-task percentiles.
- **Least privilege per role, one Confluent service account each**: producer has a Kafka key (`DeveloperWrite` on the topic) and no AWS permissions; query has a Global key (`DeveloperRead` on the topic and Schema Registry) and `cloudwatch:PutMetricData` restricted by `cloudwatch:namespace`; control has a read-only Cloud key (`MetricsViewer`, `DeveloperRead` on the topic), the access token and `cloudwatch:GetMetricData`. A fourth account, the provisioner (`EnvironmentAdmin`), makes the topic and schema for Terraform only: its keys are never in Secrets Manager or a task. `contract_test.go` and `tf/tests` check it. Only control holds the access token (it opens the UI, nothing else).
- **Env contract**: `tf/ecs.tf` `<role>_env`/`<role>_secrets` and `internal/config` agree per role (`contract_test.go`), and the design doc names every variable.
- **Access**: nothing but `/healthz` without the access token (cookie), header `X-LIGHTNING` on state changes, `/internal/*` only with the worker token and never via CloudFront, HTTPS only through CloudFront.
- **Secrets** only through Secrets Manager; `config.Config` redacts itself.
- **Charts** use the Graphite tokens (`--s1`...) with a legend and an `aria-label`; no literal colours.
- Do not state anything about the Lightning Table's API, enablement or limits that is not in the design doc's verified table with its source.

## Conventions

- Commit messages follow `.claude/skills/git-commit-message/SKILL.md`, prefixed `lightning-inventory: `, scope the area (`server`, `worker`, `frontend`, `tf`, `docker`). The image workflow is outside the asset: `repo: `.
- Review Go changes with the `go-code-reviewer` agent, and everything before a commit with the `security-reviewer` agent.
- Log through slog with sentence-case messages. Never log tokens, keys or the Launchpad URL.
- No open requests to `launchpad-dev`: the `Lightning Tables` tag is in the demo-builder vocabulary and `demos/lightning-inventory` is in `apps` in `libs/design/sync.sh` (the copy of `graphite.css` is in `frontend/src/`).
