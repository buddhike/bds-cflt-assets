# Lightning inventory

A retailer streams inventory positions (`sku`, `location_id`, `quantity`: the latest quantity
at that location) to a Kafka topic in Confluent Cloud, keyed by the SKU and location pair. A
Confluent Cloud **Lightning Table** on the topic makes the positions queryable with SQL. The
demo asks the table two questions, at a rate you set, and charts how long each takes:

* **By SKU**: the inventory of all locations for one SKU.
* **By locations**: one SKU at a set of 1 to 2000 locations.

It is a [Launchpad](../launchpad/README.md) demo: launch it from Launchpad and open the link.
How it fits together, what was verified in Confluent's documentation and what was not is in
[docs/specs/launchpad.md](docs/specs/launchpad.md).

## Using it

1. Wait for **Table: Active** in the top bar (Terraform creates the table during the launch;
   it can take a few minutes more to become active).
2. **Load catalogue** produces the whole population once: 1 million SKUs at 2,000 locations,
   each SKU stocked at about 10 of them, some 10 million positions (about 7 minutes).
3. **Inventory events**: set events per second (1 to 200, default 10) and **Start**. They
   update random positions of the catalogue.
4. **Queries**: set queries per second (1 to 1000, default 50, split evenly between the two
   scenarios) and **Start**. The charts show p50, p95 and p99 per second for 5, 15 or 60
   minutes. A few percent of queries ask for SKUs that do not exist.
5. **Reported by Confluent** shows the table's compute, scanned bytes and storage from the
   Metrics API. It is usage, not latency, and arrives minutes late.

Nothing starts by itself. Latency is measured by the query tasks (request sent to answer fully
read), in the same AWS region as the cluster, so it includes the network.

## Scaling

Three ECS services run one image: `control` (UI, API, the table's status), `producer` and `query`. The
last two scale out on CPU (up to 4 and 8 tasks) and split the rate you set between them.

## The Lightning Table

Terraform creates the table (`confluent_rtce_topic`) and removes it on destroy, so there is no
Launchpad shutdown hook. The control task only reads the table's status, to show it and to keep
the loads off until it is active. Four Confluent service accounts keep each task to what it
uses (see the design doc, "Confluent permissions").

## The image

`.github/workflows/lightning-inventory-image.yml` publishes `ghcr.io/<owner>/lightning-inventory`
on every push, tagged with the bare commit SHA, the branch name and `latest`. Launchpad passes
the commit as `launchpad_ref`, which Terraform uses as the tag. A GHCR package starts private:
make it public once, or set `registry_credentials_secret_arn`.

## Develop

```sh
go vet ./... && go test -race ./...
(cd frontend && npm ci && npm run build)
(cd tf && terraform init -backend=false && terraform validate && terraform fmt -check -recursive && terraform test)
docker build -f docker/Dockerfile -t lightning-inventory .
```
