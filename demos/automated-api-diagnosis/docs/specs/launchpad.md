# Automated API diagnosis as a Launchpad demo

The demo is a [Launchpad](../../../launchpad/README.md) demo. Launchpad runs `tf/`, which
creates the Confluent Cloud and Flink resources and deploys the app into ECS behind
CloudFront. It began as a Wails desktop app, which was removed once Launchpad could launch
the web UI.

## What runs where

```
Browser ─HTTPS─► CloudFront ─VPC origin─► internal ALB :80 ──► ECS task (Fargate): app :8080
                                                               simulator + monitor + web UI + API  (Go, ghcr.io/…/automated-api-diagnosis)
                                                                  │ produce                 ▲ consume
                                                                  ▼                         │
 Confluent Cloud   gateway-logs ──► Flink: detect_anomalies ──► api_unhealthy_events ──► (monitor tab)
                                                  │
                                                  └──► api_anomalies ──► Flink: EmitAfterGap ──► AI agent ──► Bedrock
                                                                                                   │
                                                          api_anomaly_ai_reports ──► (AI reports tab)
```

* The app is one Go binary with the Svelte build embedded. It holds the simulator, which
  produces Avro gateway logs to `gateway-logs`, and two readers that follow
  `api_unhealthy_events` and `api_anomaly_ai_reports` from their tails.
* The app's state lives in memory: the API settings, and the last 500 monitor events and
  200 reports (so a tab opened later catches up; the Flink job emits rows for every
  10-second window, so the last 500 hold each API's latest state). A replaced task starts
  from the defaults.
* **Known limit.** The readers start at the tail of their topics and the backlog is not
  stored. If the task is replaced, or a reader is down for a while, the monitor shows an
  API as healthy until its next window arrives, at most ten seconds later. The AI reports
  tab loses the reports it has not seen: a report is written once per API and status per
  five minutes, so a missed one is not repeated at once.

## Setup, in order

All of it is Terraform, run by Launchpad. There is nothing for the app to set up.

1. Confluent Cloud: environment (with Schema Registry), cluster, service account and its
   role bindings, Kafka, Schema Registry and Flink API keys.
2. After 30 seconds for the keys to take effect: the `gateway-logs-value` Avro schema,
   then the topic. The schema comes first so Flink never sees the topic without one.
3. Flink: a compute pool, then in order `alter_watermark` (the logs' own `timestamp` is
   the event time), `create_unhealthy_events`, `create_anomaly_reports`, and
   `detect_anomalies`, the long-running `INSERT` that scores each API's unhealthy rate
   per 10-second window with `ML_DETECT_ANOMALIES_ROBUST`. It starts from the first
   record, because the detector needs 30 windows (five minutes) of history. The SQL is in
   `tf/flink/`. `create_anomalies` makes the `api_anomalies` table from it.
   Then the agent (`tf/ai.tf`, see below): the IAM user and key, the Bedrock connection, the
   `EmitAfterGap` artifact and function, the model, the agent, and `ai_reports`, a second
   long-running `INSERT`.
4. AWS: network, load balancer, CloudFront, secret, task definition and service. The
   service waits for the two tables, so the app's readers always have a topic.
   Terraform waits until the load balancer sees `/healthz`.

The app starts the simulation by itself (`APIDIAG_AUTOSTART`, on by default), so the
detector has a baseline by the time anyone opens the page. After about five minutes, break
an API.

## The AI agent

`tf/ai.tf` deploys an agent that runs on Amazon Bedrock: model `openai.gpt-oss-120b-1:0`
in `ap-southeast-2` (variables `bedrock_model_id`, `bedrock_region`). The AWS account needs
model access enabled in the Bedrock console, and the runner's role (`DemoDeployPolicyArn`)
must be able to create an IAM user and its key, which are the only IAM this demo makes
beyond the ECS execution role.

* **Credential.** Terraform creates an IAM user whose only permission is `bedrock:InvokeModel`
  on that one model, and an access key for it. The key goes from `aws_iam_access_key` into
  `confluent_flink_connection` (type `BEDROCK`), both sensitive, so Terraform does not print
  it in its output (the runner must not set `TF_LOG`, whose debug levels can log requests). It is not in the repository, not an output, not in a Flink statement, not in
  the task. It is in Terraform's state in Launchpad's bucket, like the demo's other secrets,
  and destroy deletes the user and key.
* **Flow.** `create_anomalies.sql` copies the anomalous rows (not the `RECOVERED` ones) of
  `api_unhealthy_events` into `api_anomalies`. `ai_reports.sql` passes them through
  `EmitAfterGap`, which lets through the first row per API and HTTP status and then none
  for five minutes (wall clock), so a sustained failure gets one report. Each row goes to
  `AI_RUN_AGENT('anomaly_investigation_agent', ...)` and the answer is written to
  `api_anomaly_ai_reports`, whose columns are fixed in `tf/flink/create_anomaly_reports.sql`
  and all `NOT NULL`. The model, agent and prompt are in `tf/flink/ai_model.sql.tftpl` and
  `ai_agent.sql`. The agent is told to make up its findings: it has no tools and sees
  nothing of the infrastructure.
* **`EmitAfterGap`** is a Java process table function, a Maven project in
  `udf/emit-after-gap/` (Flink 2.3, Java 11, with unit tests). Terraform uploads its jar
  (`tf/udf/emit-after-gap.jar`) as a Confluent artifact and registers it with
  `CREATE FUNCTION`. The jar is committed because the runner has no Java toolchain.
  `udf/emit-after-gap/build.sh` tests and builds it in a pinned Maven image, so the same
  source gives the same bytes, and a CI workflow fails if the committed jar is not what the
  source builds. Terraform registers the function once, so a new jar reaches a running demo
  only after teardown and a new launch.
* The first report appears after the detector has its five-minute baseline, an API is
  broken, and the model answers.

## Shutdown

Nothing needs undoing before `terraform destroy`: Terraform owns everything the demo makes,
and the app creates nothing. So there is no `launchpad_shutdown` output. Terraform deletes
the Flink statements before the compute pool, and the service before the cluster.

## Contract between Terraform and the app

Environment of the app (secrets come from Secrets Manager). `internal/server/contract_test.go`
checks that Terraform sets exactly what the server reads.

| Variable | Secret | Meaning |
|---|---|---|
| `APIDIAG_ACCESS_TOKEN` | yes | Required to use the UI. See below |
| `APIDIAG_AWS_REGION`, `APIDIAG_DEMO_NAME`, `APIDIAG_ENVIRONMENT_ID`, `APIDIAG_KAFKA_CLUSTER_ID` | | Shown in the page header |
| `APIDIAG_KAFKA_BOOTSTRAP` | | `host:port` |
| `APIDIAG_KAFKA_API_KEY`, `APIDIAG_KAFKA_API_SECRET` | yes | |
| `APIDIAG_SCHEMA_REGISTRY_URL`, `APIDIAG_SCHEMA_REGISTRY_API_KEY`, `APIDIAG_SCHEMA_REGISTRY_API_SECRET` | key and secret | |
| `APIDIAG_TOPIC` | | The simulator's topic. Default `gateway-logs` |
| `APIDIAG_AI_REPORTS` | | `true` when the agent is deployed, which `ai.tf` always does. Default `false` |
| `APIDIAG_AUTOSTART` | | Start the simulation at start-up. Default `true` |
| `APIDIAG_LISTEN` | | Default `:8080` |

## Access

The UI controls a simulator that writes to a billed cluster, so it is not open. Everything
goes over HTTPS: a CloudFront distribution answers on its own `*.cloudfront.net` name and
certificate (the demo needs no domain), redirects HTTP to HTTPS, caches nothing, and passes
everything on to the app. It reaches the load balancer through a VPC origin, so the load
balancer is internal, with no address on the internet. The one plain HTTP hop stays inside
the VPC. Nothing can reach the task but the load balancer.

Terraform makes a random access token, and `launchpad_url` is
`https://<domain>.cloudfront.net/?access=<token>`. The server sets a `Secure`, `HttpOnly`
cookie from it (it knows the viewer used HTTPS from the `CloudFront-Forwarded-Proto`
header) and redirects to a URL without the token. Anything else gets 401, except
`/healthz`, which is for the load balancer. Requests that change something also need the
`X-APIDIAG` header. The UI cannot ask the simulator for more than 100 events a second per
API.

## Image

`.github/workflows/automated-api-diagnosis-image.yml` builds the image and publishes it to
the GitHub Container Registry as `ghcr.io/<owner>/automated-api-diagnosis`, tagged with the
commit SHA and `latest`. The package starts private. Make it public once (Package settings
→ Change visibility), or give Terraform `registry_credentials_secret_arn`.

## Regions

Flink must be available in the region Launchpad runs in. Check with
`confluent flink region list --cloud AWS`.
