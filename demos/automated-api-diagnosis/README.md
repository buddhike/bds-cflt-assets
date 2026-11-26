# Automated API diagnosis

Ten simulated API gateway services write Kong-style access logs to Confluent Cloud. Flink
finds the APIs whose error rate turns anomalous, a service health view shows them as they
change, and an AI agent can write up why. You can break an API (more 500s, 403s or 404s,
slower responses, an unreachable upstream) and cascade the failure to the APIs that depend
on it.

It is a [Launchpad](../launchpad/README.md) demo: launch it from Launchpad and open the
link. Terraform deploys everything, including the Flink statements. How it fits together is
in [docs/specs/launchpad.md](docs/specs/launchpad.md). [SPEC.md](SPEC.md) is the original brief.

## Using it

* **Simulator.** One tile per API. Move the sliders, toggle *Gateway error*, or choose
  what it *Depends on*: a failing dependency fails its dependents 90% of the time.
  *Start/Stop simulation* and *Reset all to defaults* are at the top. The simulation
  starts by itself.
* **Service health** (right of the tiles). A tile turns red when Flink flags the API. Flink
  needs about five minutes of normal traffic first.
* **AI reports.** One report per anomaly, if an AI agent is deployed.

## The AI agent

An agent on Amazon Bedrock (`openai.gpt-oss-120b-1:0` in `ap-southeast-2`, set by the
`bedrock_model_id` and `bedrock_region` variables) writes up each anomaly. The AWS account
needs access to that model enabled in the Bedrock console. Terraform makes the credential:
an IAM user that may invoke only that model, whose key goes straight into the Flink
connection. No credential is in the repository, and none is printed. See [tf/ai.tf](tf/ai.tf).
The agent writes a report for an API and HTTP status at most once every five minutes. That
limit is `EmitAfterGap`, a custom Flink function whose Java project is in
[udf/emit-after-gap](udf/emit-after-gap). A GitHub workflow publishes its jar as a release
and Terraform downloads it, so there is nothing to build or commit by hand.

## Develop

```sh
go vet ./... && go test -race ./...                    # unit tests
cd frontend && npm ci && npm run build                 # the UI, which the server binary embeds
cd tf && terraform init -backend=false && terraform test
docker build -f docker/Dockerfile -t automated-api-diagnosis .
```

The tests need no Confluent Cloud account.
