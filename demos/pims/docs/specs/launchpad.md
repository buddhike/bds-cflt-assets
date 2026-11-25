# PIMS as a Launchpad demo

PIMS also runs as a [Launchpad](../../../launchpad/README.md) demo. Launchpad
runs `tf/`, which creates the Confluent Cloud resources and deploys PIMS into
ECS behind a load balancer. The desktop app is unchanged.

## What runs where

```
Browser ── ALB :80 ──► ECS task (Fargate, awsvpc, one network namespace)
                         ├── pims-server   :8080  simulator + web UI + API   (Go, ghcr.io/…/pims-server)
                         ├── mysql         :3306  MySQL 8.4, binlog on        (public.ecr.aws/docker/library/mysql)
                         ├── connect       :8083  Kafka Connect + Debezium    (ghcr.io/…/pims-connect)
                         └── duckdb        :8081  DuckDB CLI + agent          (ghcr.io/…/pims-duckdb)
                       Debezium ─► Confluent Cloud Kafka ─► Tableflow (Iceberg)
```

* The four containers share a network namespace, so they reach each other on
  `127.0.0.1`. Only 8080 is reachable from the load balancer.
* `pims-server` is one Go binary with the React build embedded. The same
  binary, started as `pims-server duckdb-agent`, is the DuckDB container's
  entry point.
* MySQL lives on the task's ephemeral storage. The demo's data is lost if the
  task is replaced. Debezium state is keyed by the **task id** (the "MySQL
  generation" of the desktop app), so a replaced task snapshots again under a
  new connector name instead of resuming offsets that no longer exist.

## Setup, in order

Terraform (run by Launchpad):

1. Confluent Cloud: environment, cluster, service account, API keys, compacted
   topics. Network, load balancer, secrets, cluster, task definition.
2. ECS service. Terraform waits until the load balancer sees `/healthz`.
3. A wait for the `<topic>-value` subjects in Schema Registry
   (`tf/scripts/wait_for_subjects.py`). They appear only when Debezium has sent
   a topic's first record, which `pims-server` triggers on its own.
4. Tableflow on each topic.
5. An SSM parameter is set to `true`.

`pims-server` at start-up runs the desktop app's setup steps, with these
differences: there is no Docker and no Terraform step. It waits for MySQL,
Connect and the DuckDB agent, installs the schema, registers the Debezium
connector, starts the simulator, waits for the subjects, then waits for the SSM
parameter before it starts the Tableflow metrics monitor and lets the query
window attach the Tableflow catalog.

## Contract between Terraform and `pims-server`

Environment of `pims-server` (secrets come from Secrets Manager):

| Variable | Secret | Meaning |
|---|---|---|
| `PIMS_ACCESS_TOKEN` | yes | Required to use the UI. See below |
| `PIMS_AWS_REGION` | | |
| `PIMS_CC_API_KEY`, `PIMS_CC_API_SECRET` | yes | The launching user's Cloud API key, for the Metrics API |
| `PIMS_ORGANIZATION_ID`, `PIMS_ENVIRONMENT_ID`, `PIMS_KAFKA_CLUSTER_ID` | | |
| `PIMS_KAFKA_BOOTSTRAP` | | `host:port` |
| `PIMS_KAFKA_API_KEY`, `PIMS_KAFKA_API_SECRET` | secret | |
| `PIMS_SCHEMA_REGISTRY_URL`, `PIMS_SCHEMA_REGISTRY_API_KEY`, `PIMS_SCHEMA_REGISTRY_API_SECRET` | secret | |
| `PIMS_TABLEFLOW_API_KEY`, `PIMS_TABLEFLOW_API_SECRET`, `PIMS_TABLEFLOW_CATALOG_ENDPOINT` | secret | |
| `PIMS_TOPICS` | | comma separated |
| `PIMS_MYSQL_ROOT_PASSWORD`, `PIMS_MYSQL_APP_PASSWORD`, `PIMS_MYSQL_DEBEZIUM_PASSWORD` | yes | |
| `PIMS_AGENT_TOKEN` | yes | Shared with the DuckDB container |
| `PIMS_TABLEFLOW_PARAM` | | SSM parameter that becomes `true` once Tableflow is on |
| `PIMS_LISTEN`, `PIMS_MYSQL_ADDR`, `PIMS_CONNECT_URL`, `PIMS_DUCKDB_AGENT_URL` | | Default to `:8080`, `127.0.0.1:3306`, `http://127.0.0.1:8083`, `http://127.0.0.1:8081` |

## Access

The load balancer speaks HTTP and the UI can run SQL in a session that holds the
Tableflow credentials, so the UI is not open. Terraform makes a random access
token, and `launchpad_url` is `http://<alb>/?access=<token>`. The server sets
an `HttpOnly` cookie from it and redirects to a URL without the token. Anything
else gets 401, except `/healthz`. The token travels unencrypted, so use
`allowed_cidr_blocks` to limit who can reach the load balancer, or put a
certificate on it, for anything beyond a short-lived demo.

## Images

`.github/workflows/pims-images.yml` builds three images and publishes them to
the GitHub Container Registry as `ghcr.io/<owner>/pims-server`, `pims-connect`
and `pims-duckdb`, tagged with the commit SHA and `latest`. Packages start
private. Make them public once (Package settings → Change visibility), or give
Terraform `registry_credentials_secret_arn`.
