# PIMS as a Launchpad demo

PIMS also runs as a [Launchpad](../../../launchpad/README.md) demo. Launchpad
runs `tf/`, which creates the Confluent Cloud resources and deploys PIMS into
ECS behind a load balancer. It is the only way to run PIMS.

## What runs where

```
Browser ─HTTPS─► CloudFront ─VPC origin─► internal ALB :80 ──► ECS task (Fargate, awsvpc, one network namespace)
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
  generation"), so a replaced task snapshots again under a
  new connector name instead of resuming offsets that no longer exist.

## Setup, in order

Terraform (run by Launchpad):

1. Confluent Cloud: environment, cluster, service account, API keys, compacted
   topics. Network, load balancer, secrets, cluster, task definition.
2. ECS service. Terraform waits until the load balancer sees `/healthz`.

Terraform does not enable Tableflow. A topic needs a schema first, and Debezium
registers a schema only when it sends the topic's first record, which is the
app's doing. The app enables Tableflow itself.

`pims-server` at start-up runs the setup steps. There is no Docker and no
Terraform step. It waits for MySQL,
Connect and the DuckDB agent, installs the schema, registers the Debezium
connector, starts the simulator and waits for the `<topic>-value` subjects in
Schema Registry. It then enables Tableflow on each topic through the Confluent
Cloud API (`POST /tableflow/v1/tableflow-topics`, as Iceberg tables in managed
storage, signed in with the Tableflow API key that Terraform made). A topic that
already has Tableflow is left alone, and a refusal is tried again for a while,
because a schema registered a moment ago can still be unknown to Tableflow.
Last it starts the Tableflow metrics monitor and lets the query window attach
the Tableflow catalog.

## Shutdown

Terraform owns the topics, and the app switched Tableflow on, so the app switches
it off. Before Launchpad runs `terraform destroy` it calls the demo's shutdown
hook (`launchpad_shutdown`, see Launchpad's design doc):

```
POST https://<cloudfront domain>/shutdown
Authorization: Bearer <access token>
```

The token is the access token. The cookie does not work here, and neither does the
token in the address. The app stops its setup, the simulator and the monitor, then
disables Tableflow on each topic (`DELETE /tableflow/v1/tableflow-topics/<topic>`,
which leaves the Kafka topic alone; a topic without Tableflow counts as done).

| Answer | Meaning |
|---|---|
| `200` | Tableflow is off. Launchpad destroys |
| `500` with `{"error": "..."}` | The app tried and failed, or its setup would not stop. Launchpad does not destroy, and shows the message |
| `401` | Wrong token |

It is `500` and not `502` on purpose: CloudFront and the load balancer answer 502,
503 and 504 when the app is not there, and Launchpad destroys the demo anyway then.
The call can be repeated, so a failed teardown can be retried, and it answers within
50 seconds, inside the 60 that CloudFront waits for an origin. After a shutdown the app
does not start the demo again, and the process is expected to be destroyed next.

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
| `PIMS_LISTEN`, `PIMS_MYSQL_ADDR`, `PIMS_CONNECT_URL`, `PIMS_DUCKDB_AGENT_URL` | | Default to `:8080`, `127.0.0.1:3306`, `http://127.0.0.1:8083`, `http://127.0.0.1:8081` |

## Access

The UI can run SQL in a session that holds the Tableflow credentials, so it is not
open. Everything people and Launchpad send to the demo goes over HTTPS: a CloudFront
distribution answers on its own `*.cloudfront.net` name and certificate (so the demo
needs no domain), redirects HTTP to HTTPS, caches nothing, and passes everything on to
the app. It reaches the application load balancer through a VPC origin, so the load
balancer is internal, has no address on the internet, and lets in only the security group
CloudFront puts in the VPC. The one hop that is plain HTTP, from CloudFront to the load
balancer, stays inside the VPC. Nothing can reach the task but the load balancer.

Terraform makes a random access token, and `launchpad_url` is
`https://<domain>.cloudfront.net/?access=<token>`. The server sets a `Secure`,
`HttpOnly` cookie from it (it knows the viewer used HTTPS from the
`CloudFront-Forwarded-Proto` header) and redirects to a URL without the token. Anything
else gets 401, except `/healthz`, which is for the load balancer, and `POST /shutdown`,
which wants the token as a bearer token. There is no IP allow list: CloudFront would
need a WAF web ACL in `us-east-1` for that.

## Images

`.github/workflows/pims-images.yml` builds three images and publishes them to
the GitHub Container Registry as `ghcr.io/<owner>/pims-server`, `pims-connect`
and `pims-duckdb`, tagged with the commit SHA and `latest`. Packages start
private. Make them public once (Package settings → Change visibility), or give
Terraform `registry_credentials_secret_arn`.
