# Lightning inventory as a Launchpad demo

Status: **built, not yet run in the cloud.** Terraform owns the Lightning Table and everything else: there is no shutdown hook (owner's decision, 2026-11-27). Everything about Confluent Cloud Lightning Tables below was read from Confluent's documentation on 2026-11-27 (sources in "Verified facts"). What the documentation does not say is listed under "Not documented", and the demo does not assume an answer to it.

The demo is a [Launchpad](../../../launchpad/README.md) demo. Launchpad runs `tf/`, which creates the Confluent Cloud resources and deploys the app into ECS behind CloudFront.

## Purpose and scenario

Demonstrate the query capabilities and performance of Confluent Cloud Lightning Tables.

A retailer streams real-time inventory positions to a Kafka topic. A record has `sku`, `location_id` and `quantity` (the latest quantity at that location). The API they build on the Lightning Table answers two questions, and the demo measures each:

1. **By SKU** (`by_sku`): the inventory of all locations for one `sku`.
2. **By locations** (`by_locations`): the inventory of one SKU at a given set of locations, 1 to 2000 `location_id`s. See "Interpretation of the second scenario".

## Verified facts about Lightning Tables

Sources are Confluent's documentation pages under `https://docs.confluent.io/cloud/current/`, read 2026-11-27.

| Fact | Source |
|---|---|
| A Lightning Table materialises a topic for low-latency SQL queries. It is updated within 100 ms of data arriving in the topic. Enabling it takes several minutes. | `lightning-tables/overview.html`, `lightning-tables/manage.html` |
| Available on Basic, Standard, Enterprise and Dedicated clusters, **AWS only**, in some regions. Per the region tables (`clusters/regions.html`): us-east-1, us-east-2, us-west-2, ap-northeast-1, ap-northeast-2, ap-southeast-2, ap-south-1, ap-southeast-1, eu-central-1, eu-west-1, eu-west-2. `tf/` rejects any other `aws_region` in a precondition. | `lightning-tables/limitations.html`, `clusters/regions.html` |
| The topic needs a registered schema (Avro, Protobuf or JSON Schema). | `lightning-tables/limitations.html` |
| **Modes**: append (default) or **upsert**, chosen automatically from the topic's `cleanup.policy`. With `compact` the table keeps the latest record per Kafka record key, which is the primary key. The key may be raw bytes or schema-based. A record with a null value deletes the key. | `lightning-tables/limitations.html` |
| **Enable (REST)** (documented; the app does **not** call it, Terraform does it): `POST https://api.confluent.cloud/rtce/v1/rtce-topics`, HTTP Basic with a Cloud or Global API key, body `{"spec":{"cloud":"AWS","region":..,"topic_name":..,"environment":{"id":..},"kafka_cluster":{"id":..},"description":..}}`. Answers **202** (being created), **409** if it is already there. `topic_name` must match `^[a-zA-Z][a-zA-Z0-9_]*$` (no hyphen). | `lightning-tables/manage.html`, API reference `ccloud/create-rtce-v-1-rtce-topic/` |
| **Read**: `GET /rtce/v1/rtce-topics/{topic_name}?environment=<env>&spec.kafka_cluster=<lkc>`. 200 with `status.phase` in PENDING, PROVISIONING, ACTIVE, DELETING, FAILED, UNAVAILABLE (and `status.error_message`); 404 when there is none. A table is queryable only when ACTIVE. | API reference `ccloud/get-rtce-v-1-rtce-topic/`, `lightning-tables/manage.html` |
| **Disable (REST)** (documented; not called by the app): `DELETE /rtce/v1/rtce-topics/{topic_name}?environment=<env>&spec.kafka_cluster=<lkc>`. **204** (being deleted), 404 when there is none. | API reference `ccloud/delete-rtce-v-1-rtce-topic/` |
| **Terraform enables and disables the table**: `resource "confluent_rtce_topic"` with `cloud` ("AWS"), `region`, `topic_name`, `description`, `environment { id }`, `kafka_cluster { id }` (all required in provider 2.96.0, where `description` is required although the manage page calls it optional), plus a `timeouts { create, delete }` block (schema of the provider binary). Exports `api_version`, `kind`, `resource_name`. It has no `credentials` block: it uses the provider's own Cloud API key. To disable, remove the resource and apply. The provider docs mark it General Availability. | `lightning-tables/manage.html` ("Terraform" under enable and disable), provider docs `docs/resources/confluent_rtce_topic.md`, `terraform providers schema` of 2.96.0 |
| **Who may manage a table** (enable, disable, edit the description): `OrganizationAdmin`, `EnvironmentAdmin`, `CloudClusterAdmin`, or `ResourceOwner` on the topic. "A Cloud API key or Global API key for Terraform access." Here Terraform runs with the launching user's Cloud API key (Launchpad's `confluent_cloud_api_key`). | `lightning-tables/access-control.html`, `lightning-tables/manage.html` (Prerequisites) |
| **Who may query**: `OrganizationAdmin`, `EnvironmentAdmin`, `CloudClusterAdmin` or `DeveloperRead` (cluster or topic scope; for MCP the table lists DeveloperRead on the cluster or the topic as "Query data"). The OAuth path names DeveloperRead on the Kafka cluster and on the Schema Registry cluster. | `lightning-tables/access-control.html` |
| **Query (REST)**: `POST https://sql.<region>.aws.confluent.cloud/query/v1`, HTTP Basic with a **Global API key**, body `{"catalog_name":"<env id>","database_name":"<lkc id>","query":"<SQL>","options":{"max_result_rows":N,"result_format":"JSON"}}`. The table is named by the topic name (backticks when it has non-alphanumerics). 200 returns the result inline (`result.data` is an array of rows of strings); **202** means the server decided the result is large or slow, and it must be polled (`/query/v1/jobs/.../status`) and downloaded from a presigned URL. The client cannot choose which. 404 = table not available yet; 429 = rate limit. | `lightning-tables/query/api.html` |
| **SQL**: key lookups, `=`, `!=`, comparisons, `IN`, `NOT IN`, `IS NULL`, `LIKE`, `BETWEEN`, `AND`/`OR`, column selection, `ORDER BY`, `LIMIT`. No aggregates, `GROUP BY`, joins, subqueries. | `lightning-tables/query/sql-reference.html` |
| **Auth for queries**: a Global API key owned by a principal with `DeveloperRead` on the topic and on the Schema Registry cluster. The page says a Global API key works on all supported cluster types "including Basic and Standard", while the general Global API key page says Global keys do not support Basic and Standard Kafka clusters (that statement concerns the Kafka data plane; this demo uses the Kafka key, not the Global key, for Kafka). | `lightning-tables/access-control.html`, `security/.../api-keys/overview.html` |
| **Global API key from Terraform**: `confluent_api_key` with `managed_resource { id = "global", api_version = "global/v1", kind = "Global" }`, owned by a service account (two per account at most). | provider docs `docs/resources/confluent_api_key.md`, CHANGELOG |
| **Rate limit**: queries share a limit with the Real-Time Context Engine MCP tools; over it the endpoint answers 429. The value is not published. Job-status requests are exempt. No row limit through the API; a result has a maximum size. | `lightning-tables/query/api.html` |
| **Confluent-published metrics** exist, through the Metrics API (`POST https://api.telemetry.confluent.cloud/v2/metrics/cloud/query`, Cloud API key of a service account with `MetricsViewer`, bindable at organisation, environment or cluster scope): `io.confluent.rtce/compute_utilization/ccu_minutes`, `.../dp_scanned_bytes` (counters) and `io.confluent.rtce/storage_utilization/storage_bytes` (gauge), per cluster, groupable by `metric.topic`. They report **usage (compute, bytes scanned, storage), not query latency.** | `lightning-tables/monitor.html`, `security/access-control/rbac/predefined-rbac-roles.html` |
| CloudWatch: `PutMetricData` takes `Values` and `Counts` (up to 150 values per metric per call) and supports percentile statistics on them; `StorageResolution = 1` makes a high-resolution metric, readable with a period of 1, 5, 10, 30 or a multiple of 60 s, **retained at sub-minute periods for 3 hours**. A request carries at most 1000 metrics and 1 MB. `GetMetricData` takes up to 500 queries and 100,800 data points. | AWS docs: `API_PutMetricData`, `API_GetMetricData`, "Metrics concepts" |

### Not documented (no assumption is made)

1. **How a key schema's fields appear as columns**, and whether `WHERE sku = ...` on a key field or a value field uses an index or scans. The docs list "key lookups" without saying how a composite or raw key is addressed. The demo therefore keeps `sku`, `location_id` and `quantity` as ordinary **value** columns and uses a **raw string key** (`<sku>|<location_id>`), and queries filter on the value columns. Whether the by-SKU query is index-accelerated is exactly what the latency graph will show. If Confluent documents a way to address a composite key (or a secondary index), the query text in `internal/lightning/queries.go` is the one place to change.
2. **Whether enabling a table backfills records already in the topic.** The UI therefore only allows loading the catalogue when the table is ACTIVE.
3. **The numeric rate limit**, the **maximum SQL length** (a 2000-location `IN` list is about 22 KB), and whether `IN` has a size limit. Errors are counted per scenario, so a limit shows as an error rate.
4. How long enabling and deleting take (documented only as "several minutes").
5. **Whether the provider waits for the table to be ACTIVE** before `terraform apply` returns. The resource has create and delete timeouts, which suggests it waits, but nothing says so. The control task reads the phase and the UI gates the loads on ACTIVE either way.
6. **Which role reads a table's status** (`GET /rtce/v1/rtce-topics/{name}`). The documents list the roles that manage and that query, not that view. The control task's service account has `DeveloperRead` on the topic (and `MetricsViewer` on the cluster); if the read answers 403 the UI shows the error and never opens the gate. See the first-launch checklist.
7. **Whether the topic can be deleted the moment the table's delete returns.** Terraform destroys the table first (the workers wait on it, it waits on the topic), then a `time_sleep` of 60 s, then the topic.
8. Cost: only "usage-based billing" (compute, scanned bytes, storage). Scanned bytes grow with query rate and with how many rows a query scans.

## Decision on who enables the table

**Terraform enables it** (`confluent_rtce_topic`; decided by the owner on 2026-11-27, superseding the earlier decision that the app enables it and a `launchpad_shutdown` hook disables it). Consequences:

* `terraform destroy` removes the table in order, and `terraform plan` shows it. Per the demo-builder skill (section 5), a demo whose Terraform owns everything has **no hook**: no `launchpad_shutdown` output, no `POST /shutdown`.
* The app never writes to Confluent's control plane. The control task only **reads** the table's status (`GET`), to show the phase and to gate the loads on ACTIVE (Terraform may return before ACTIVE: not documented).
* The table is created with the provider's own credentials (the launching user's Cloud API key). No task holds a key that could enable, disable or edit a table, and none holds an administrator role (see "Confluent permissions").
* `contract_test.go` requires `confluent_rtce_topic` in `tf/` and fails on a `launchpad_shutdown` output, a `/shutdown` route, or an Enable or Disable in `internal/lightning`.
* **Order.** Create: schema, topic, table, then the worker services (they wait for it; Terraform may or may not wait for ACTIVE). Destroy, the exact reverse: worker services stop (no load while the table goes), the table, a 60 s pause (`time_sleep.topic_settle`, because a topic that still backs a table may refuse to be deleted and nothing documents whether the table's delete has finished when the provider returns), then the topic, the schema, the service accounts' keys and roles. The control service has no dependency on the table: it stays up and shows DELETING.
* **Nothing the app creates blocks `terraform destroy`.** The app's only side effects are records in the topic (deleted with it), CloudWatch metric data (a namespace, not a resource, so nothing to delete), log lines (in a log group Terraform owns) and in-memory state. It creates no Confluent, AWS or other resource. Teardown therefore needs no human and no call to the app, and it works when the app is already gone.

## What runs where

```
Browser ─HTTPS─► CloudFront ─VPC origin─► internal ALB :80 ─► ECS service "control" (1 task, :8080)
                                                              UI, control API, access gate
                                                              reads the table's status, CloudWatch + Metrics API (read-only)
                                                                ▲  poll /internal/heartbeat (worker token, via the ALB)
                              ┌─────────────────────────────────┴───────────────┐
        ECS service "producer" (1..N tasks)                         ECS service "query" (1..N tasks)
        live events + catalogue seeding                             by_sku and by_locations load
        ── produce (Avro value, raw key) ─► topic                   ── POST /query/v1 ─► Lightning Table
                                                                    ── PutMetricData (1 s, Values/Counts) ─► CloudWatch
Confluent Cloud: environment, Standard cluster (AWS), topic `inventory_positions` (6 partitions, compact), Avro schema,
                 Lightning Table (confluent_rtce_topic, made and removed by Terraform)
```

One image, one binary. `LIGHTNING_ROLE` is `control`, `producer` or `query`.

* **Control** is its own service (not part of a worker) so that scaling workers never restarts the UI, there is one place that holds the rates. It holds state in memory (a replaced task starts from the defaults: loads stopped, default rates, catalogue not loaded).
* **Workers poll the control** once a second: `POST /internal/heartbeat` with the worker token, carrying their counters, getting back whether to run and their **share** of the rate. Control counts the workers that polled in the last 5 s and tells each `total / workers`. A worker that vanishes is dropped from the count after 5 s, and a new one joins the next second, so the total stays what the operator set while autoscaling adds or removes tasks. **The cap and membership changes:** a worker that control cannot reach stops on its own after **3 s** (`StaleAfter`), and a poll gives up after **1 s** (`PollTimeout`). Control last heard the worker at some moment T; the worker's last good poll is at most `PollTimeout` after T, so it stops by T + 4 s, strictly before control stops counting it (T + 5 s, `aliveFor`) and hands its share to the others. A cut-off worker never adds to the total (a test models this). In the other direction, for up to about **a second** after a task joins or leaves, the total can be above the set rate (the new task is counted at once; the others learn their smaller share at their next poll). The per-task bounds (200 events/s, 1000 queries/s, 512 in flight) hold at all times. A rate under 1 per second per task (for example 1 event/s over 2 producers, 0.5 each) works: the pacer's credit allows one call more than a second of rate. The counts of a heartbeat that did not get through ride on the next one, for up to 30 s. **Rare double count:** a heartbeat that control received but whose reply timed out on the worker's side is counted by control and sent again with the next one, so those counts (a second of events or queries in the UI's rates) appear twice. The CloudWatch latency data is not affected (it does not go through the heartbeat). Workers reach control through the internal ALB (security group rule from the worker group only). The `/internal/` paths refuse a request that came through CloudFront.
* **Scaling.** Each worker service has ECS service auto scaling on average CPU (target 60 %, min 1, max `producer_max_tasks` 4 and `query_max_tasks` 8), and the rate is split between tasks as above. Max rates (200 events/s, 1000 queries/s) fit one task; the scaling is there for higher bounds and for slow responses (a query in flight for 300 ms at 1000/s means 300 requests in flight). The operator cannot set the task count from the UI: the app has no ECS permissions. Change `*_min_tasks` or the bounds in Terraform to pin a count.

## The data

* **Topic** `inventory_positions` (Avro value, schema registered by Terraform **before** the topic), **6 partitions**, `cleanup.policy=compact` so the table runs in upsert mode and keeps the latest quantity per key.
* **Key** is the string `<sku>|<location_id>`, for example `SKU-0000042|LOC-0007` (raw bytes, no schema). One key per pair, so the producer's default murmur2 partitioner puts a pair always in the same partition.
* **Value** schema `lightning.inventory.InventoryPosition`: `sku` string, `location_id` string, `quantity` int, all required (`tf/inventory-value.avsc`, checked against the Go schema by `contract_test.go`). Wire format: magic byte 0, the 4-byte schema id Terraform passes in `LIGHTNING_VALUE_SCHEMA_ID`, Avro binary.
* **Catalogue**: 1,000,000 SKUs (`SKU-0000000`..`SKU-0999999`) and 2,000 locations (`LOC-0000`..`LOC-1999`). Dense would be 2 billion pairs, which is neither loadable nor useful, so each SKU is **stocked at 1..(2n-1) locations (mean `locations_per_sku`, default 10, so about 10 million pairs)**, chosen by a fixed hash. **Deviation from the brief ("pre-generate and bake into the image")**: the catalogue is generated by a deterministic function (`internal/catalog`), not stored. It is the same on every task, costs 0 bytes in the image (a stored pair list would be about 20 MB at 2 bytes per location, plus an index), needs no load step at start, and lets any worker answer "which locations does SKU N stock" without a lookup. A test pins its first values so a change is noticed.
* **Loading** (the "Load catalogue" button, allowed when the table is ACTIVE): the 10 million records are produced once, in 64 shards that control leases to producer tasks, at a fixed server-side rate of 25,000 records/s in total (bound 50,000), so about 7 minutes. This is the one place the producer exceeds 200 events/s; the 200 events/s bound applies to the live stream the operator sets, and the load is fixed in code, one-shot, and the browser cannot change it. Reloading is harmless (upsert) but repeats the cost.
* **Live events**: each picks a random catalogue pair and a new quantity (0..500).
* **Queries** draw from the catalogue, so a key exists once the catalogue is loaded; a fixed fraction (`LIGHTNING_MISS_RATIO`, default 0.02, at most 0.2; a Terraform variable, not the browser's) uses a SKU that is not in the catalogue (`SKU-1000000` and above) to measure misses.

### Interpretation of the second scenario

The brief says "a given set of locations (1 or more)", up to 2000. Read literally (every SKU at those locations) one location holds about 5,000 rows and 2000 locations the whole 10-million-row table, which the API would return only as an asynchronous download. So `by_locations` is **one SKU at a set of 1..2000 locations** (size uniform): `WHERE sku = '…' AND location_id IN (…)`. This reads as an inventory check ("how much of this item do these stores have"). If the owner wants the literal reading, change `internal/lightning/queries.go` and expect 202 responses, which the client counts as errors (it does not poll them).

## Queries and what is measured

```sql
-- by_sku
SELECT sku, location_id, quantity FROM `inventory_positions` WHERE sku = 'SKU-0000042'
-- by_locations (k uniform in 1..2000, distinct locations)
SELECT sku, location_id, quantity FROM `inventory_positions` WHERE sku = 'SKU-0000042' AND location_id IN ('LOC-0007', …)
```

* **Latency is client-side**: from the moment the query worker starts the HTTP request to the moment the whole response body has been read, in a task in the same AWS region as the cluster. Only HTTP 200 responses count as latency samples. 429, other 4xx/5xx, network errors and 202 (asynchronous result, not followed) count as `Errors`; a query the worker could not start because its in-flight cap (512 per task) was full counts as `Dropped`. Pacing is open-loop, so a slow table does not slow the load down.
* **Published to CloudWatch** every second by each query task: namespace `LightningInventory/<demo name>` (one per deployment), dimension `Scenario` = `by_sku` or `by_locations`, metric `Latency` (Milliseconds) as `Values`/`Counts` (latencies rounded to a 5 % log scale, so at most 150 distinct values), `StorageResolution = 1`; `Errors` and `Dropped` (Count) when non-zero. **Deviation from the brief**: it asked to capture p50, p95 and p99 as the metric. Publishing the latency *distribution* lets CloudWatch compute p50, p95 and p99 over the samples of **all tasks together** for each 1 s period, which averaging per-task percentiles cannot do correctly once the service scales; the UI asks CloudWatch for `p50`, `p95`, `p99` and `SampleCount` at `Period = 1`. The 5 % rounding is the resolution of the percentiles.
* **The UI charts** come from `GetMetricData` through the control service (the browser never talks to AWS): last 5, 15 or 60 minutes at 1 s. CloudWatch keeps 1 s data for 3 hours, and shows a sample a few seconds after it happened.
* **Confluent's own metrics** (compute CCU minutes, bytes scanned, storage bytes for this table) are fetched from the Metrics API once a minute at 1-minute granularity and shown **separately** in a "Reported by Confluent" panel. They are usage, not latency, and arrive minutes late.

## Setup, in order

1. Terraform: environment (with Schema Registry), Standard cluster, then **four service accounts** (provisioner, producer, query, control) with their role bindings, and their keys: the provisioner's Kafka and Schema Registry keys (Terraform only), the producers' Kafka key, the query tasks' **Global** key, the control task's **Cloud** key; a 30 s wait for the provisioner's keys to take effect.
2. The value schema, then the topic (6 partitions, compact), both with the provisioner's keys.
3. The role bindings scoped to the topic and the other keys, a 30 s wait for those keys.
4. **The Lightning Table** (`confluent_rtce_topic`, create timeout 30 min).
5. AWS: network, load balancer, CloudFront, secrets, IAM, log group, ECS cluster. The control service (waits for `/healthz`) can start as soon as the keys are usable; the producer and query services wait for the table.
6. The control task polls the table's phase (every 5 s until ACTIVE, then every 30 s) and shows it. The operator waits for ACTIVE (a few minutes, unless Terraform already waited), clicks **Load catalogue**, then starts the events and the queries.

## Teardown (no hook)

Launchpad runs `terraform destroy`: the workers, the table, a 60 s pause, the topic and the rest, in the order above ("Decision on who enables the table"). There is no `launchpad_shutdown` output, so Launchpad does not call the app. The control task handles SIGTERM (ECS's stop) by ending its status polling and giving in-flight requests 10 s to finish; nothing needs undoing.

## Environment contract

`contract_test.go` checks `tf/ecs.tf` against `internal/config` for each role. Secrets come from Secrets Manager (one secret `<name>/config`), each role gets only its own. The provisioner's keys, which Terraform uses to create the topic and the schema, are not in it. What shrank with the hook gone: control lost the Global key; the access token is held by control only and is used only by the access gate (it no longer authenticates a teardown call); the worker token is unchanged (workers still poll control).

| Variable | control | producer | query | Notes |
|---|:-:|:-:|:-:|---|
| `LIGHTNING_ROLE` | x | x | x | `control`, `producer`, `query` |
| `LIGHTNING_AWS_REGION` | x | x | x | Cluster and AWS region |
| `LIGHTNING_DEMO_NAME` | x | x | x | Resource name suffix, page header |
| `LIGHTNING_LISTEN` | opt | | | Default `:8080` |
| `LIGHTNING_ENVIRONMENT_ID`, `LIGHTNING_KAFKA_CLUSTER_ID` | x | | x | `catalog_name`, `database_name`; the table's scope |
| `LIGHTNING_TOPIC` | x | x | x | `inventory_positions` |
| `LIGHTNING_KAFKA_BOOTSTRAP` | | x | | |
| `LIGHTNING_VALUE_SCHEMA_ID` | | x | | Avro wire format |
| `LIGHTNING_LOCATIONS_PER_SKU` | | x | x | Catalogue density; same value for both |
| `LIGHTNING_MISS_RATIO` | | | x | |
| `LIGHTNING_METRICS_NAMESPACE` | x | | x | CloudWatch namespace |
| `LIGHTNING_CONTROL_URL` | | x | x | `http://<internal ALB>` |
| `LIGHTNING_ACCESS_TOKEN` (secret) | x | | | Opens the UI. Used for nothing else (there is no hook) |
| `LIGHTNING_WORKER_TOKEN` (secret) | x | x | x | `/internal/*` only |
| `LIGHTNING_KAFKA_API_KEY`, `LIGHTNING_KAFKA_API_SECRET` (secret) | | x | | Kafka cluster key |
| `LIGHTNING_GLOBAL_API_KEY`, `LIGHTNING_GLOBAL_API_SECRET` (secret) | | | x | Queries. Global key of the query service account (`DeveloperRead`) |
| `LIGHTNING_CLOUD_API_KEY`, `LIGHTNING_CLOUD_API_SECRET` (secret) | x | | | Metrics API and the table's status. Cloud key of the control service account (`MetricsViewer`, `DeveloperRead` on the topic): read-only |

Bounds that are code, not configuration (the cluster is billed): events 1..200 per second (default 10), queries 1..1000 per second (default 50), seed rate 25,000 per second, locations per query 1..2000, 512 queries in flight per task. The browser sets only the two rates, and whether each load runs.

## Access

As in the demo-builder skill: the per-launch token in `launchpad_url` (sensitive), a `Secure; HttpOnly` cookie, 401 for everything but `/healthz`, header `X-LIGHTNING` on state changes, `/internal/*` with the worker token and never via CloudFront. There is no `/shutdown`. JSON bodies refuse unknown fields and trailing data, except that `/internal/heartbeat` ignores unknown fields (so a worker of a newer version can run beside an older control during a rolling deploy); its counts are bounded either way.

## AWS permissions of the tasks (a deliberate exception)

The skill says the app gets no AWS permissions unless it truly needs them. Two roles do, and need exactly this:

| Task role | Permission | Why |
|---|---|---|
| query | `cloudwatch:PutMetricData` on `*` with condition `StringEquals cloudwatch:namespace = LightningInventory/<name>` | Publish the latency distribution. `PutMetricData` has no resource-level scoping; the namespace condition is the restriction |
| control | `cloudwatch:GetMetricData` on `*` | Read the charts. `GetMetricData` supports no resource or namespace condition |
| producer | none | |

The execution role (image pull, secrets) is separate and unchanged from the other demos.

## Confluent permissions (four service accounts)

No task holds an administrator role. Terraform's own provider credentials (the launching user's Cloud API key) create the table.

| Service account | Role bindings | Keys | Held by |
|---|---|---|---|
| provisioner | `EnvironmentAdmin` on the demo's environment | Kafka (cluster-scoped), Schema Registry | **Terraform only**: used to create the topic and the schema. Not in Secrets Manager, not in any task |
| producer | `DeveloperWrite` on the topic | Kafka (cluster-scoped) | producer tasks |
| query | `DeveloperRead` on the topic, `DeveloperRead` on Schema Registry subjects (the registry holds this demo's one schema) | Global | query tasks |
| control | `MetricsViewer` on the cluster, `DeveloperRead` on the topic | Cloud | control task |

`tf/tests` and `contract_test.go` check the split (a single `EnvironmentAdmin`, on the provisioner; each key owned by its role's account; the provisioner's keys referenced nowhere but Terraform).

### Accepted risks

* The provisioner's `EnvironmentAdmin` is broad. It could be narrowed to `CloudClusterAdmin` on the cluster plus `ResourceOwner` on the value subject, which has not been tried against a live account; keeping the broad role on a service account whose keys only Terraform uses avoids a launch failing on an untested role. Its keys are in Terraform state like every other key, and the state lives in Launchpad's backend. The account is confined to this demo's environment, which holds nothing else, and is deleted with it.
* The control account's `DeveloperRead` on the topic is a guess at what reading a table's status needs (not documented, item 6 of "Not documented"). It is a read role and the key is a Cloud key, not the Global key the query endpoint accepts, so it gives no query access of its own. If a launch shows another role is needed, widening it is an owner decision.
* The query account reads every subject of the registry.

## Launchpad contract

Needs `aws_region`, `launchpad_tags`, both Confluent variables and `launchpad_url`. There is no `launchpad_shutdown`: Terraform owns everything. Image `ghcr.io/<owner>/lightning-inventory`, tagged with `launchpad_ref`, built by `.github/workflows/lightning-inventory-image.yml`; the same image runs all three roles. Tags in `demo.json`: `Apache Kafka`, `Lightning Tables` (in the skill's tag vocabulary). `libs/design/sync.sh` lists this demo. No request to `launchpad-dev` is open.

## Known limits

* State is in memory: replacing the control task resets the rates, stops the loads and forgets that the catalogue was loaded (the table keeps the data).
* The control task reads the table's status with a key whose role for that call is not documented (item 6 above). If the read is refused, the UI shows the error and the loads stay blocked. The read backs off from 10 s, doubling, to 60 s for a 401, 403 or other 4xx (right after an apply a new key or role may not have taken effect yet), to 5 minutes for other errors and straight to 5 minutes for a 429. The usage numbers back off the same way (1 min, to 10 min; 5 min after a 429).
* A CloudWatch outage keeps the last 120 seconds per scenario for a retry and drops older ones; a publish that timed out after reaching CloudWatch is counted twice when retried.
* One CloudWatch read serves every open page for 5 s (`/api/latency`); a range with more than 20 result pages is cut at the oldest data and flagged (`truncated`).
* **Catalogue load epochs.** Each click of **Load catalogue** starts a load with a new epoch (control's counter, in the plan and in every producer report). Control's counter starts at its start time in seconds, so a restarted control never announces an epoch an earlier one used, and a producer follows any different non-zero epoch (0 means no load). A producer that sees a new epoch forgets the shards it finished and its error count, so a second load really produces the records again and nothing of the old load is reported under the new epoch; control ignores a report that belongs to another epoch. **Records** in the UI is the sum of the records of the shards control has accepted as done from their owner, each counted once when it first becomes done: a retried shard, a resent report (lost answer) or a shard handed to another task cannot count twice, and the progress moves a shard at a time. A shard's records are queued with a context that stays alive until the flush returns: a cancelled context fails the records still buffered in the Kafka client, so it is cancelled only to abandon a shard. A producer stops producing for a shard the moment control's plan no longer lists it (not at the next 1 s lease check), and keeps checking the lease during the flush (up to 30 s); a flush that fails counts in the UI's error count.
* The live events still buffered when a producer task is told to stop are flushed for up to 5 s, not dropped.
* A failed CloudWatch read for the charts is remembered for 3 s, so a throttled CloudWatch is not asked again by every open page.
* A 202 (asynchronous) query result is counted as an error and not followed.
* CloudWatch percentiles are exact over the rounded 5 % latency buckets, and a sample appears in CloudWatch a few seconds late.
* Scaling is CPU-driven and has a floor of one task per worker service.
* Not verified against a live Confluent Cloud: everything in "Not documented", the Global key on a Standard cluster, and the real table behaviour. They are the first things to check on a first launch (below).

## Effect of an update on a running demo

An update is `terraform apply` over the live demo. **The table is in Terraform now, so `terraform plan` shows it**, and a change that replaces the topic replaces the table: `partitions` (a different `partitions_count` replaces the topic and loses its data; the table depends on the topic, so it is recreated empty and takes minutes to be ACTIVE again) and anything that renames the topic. `cleanup.policy` can change in place. A changed `description`, `region` or `topic_name` of the table: read the plan, since whether the provider updates these in place or replaces the table is not documented. The value schema is registered before the topic; a changed `.avsc` is a new schema version (compatible changes only). Moving an already-running demo from the earlier design (app-enabled table) to this one: the table already exists, so the first apply would fail with 409; `terraform import confluent_rtce_topic.inventory <env>/<lkc>/inventory_positions` first. No demo of the earlier design has been launched.

## First launch checklist (only possible in the cloud)

1. The provider creates the table (`confluent_rtce_topic`) with the launching user's Cloud key, and whether `apply` returns before the table is ACTIVE.
2. The control task's Cloud key can read the table's status (`GET /rtce/v1/rtce-topics/...`) with `DeveloperRead` on the topic and `MetricsViewer`. A 403 here needs an owner decision (a wider role) and blocks the gate.
3. Global key (query service account, `DeveloperRead` on the topic and the subjects) authenticates to `sql.<region>.aws.confluent.cloud` for a Standard cluster (the two doc pages disagree).
4. The table reaches ACTIVE and answers `by_sku` (200, rows).
5. Whether the table has the loaded data (backfill question) and how the by-sku query behaves at 10 million rows.
6. A 2000-location `IN` query is accepted (size) and its latency.
7. Rate at which 429 starts.
8. Teardown: `terraform destroy` finishes with no human: the table goes, then (after the 60 s pause) the topic deletes without complaint.
9. The producers' key (`DeveloperWrite` on the topic only) can produce, with the cluster-scoped key.
10. CloudWatch shows `Latency` percentiles at 1 s for two tasks together.
