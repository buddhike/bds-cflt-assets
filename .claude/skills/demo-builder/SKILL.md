---
name: demo-builder
description: How to build a demo that Launchpad can launch and tear down in this repository. Use when creating a new demo under demos/, converting an existing app into a Launchpad demo, changing a demo's tf/ directory, access gate, shutdown hook or image workflow, or debugging a demo that fails to launch or destroy.
---

# Building a Launchpad demo

Launchpad (`demos/launchpad`) launches a demo into an AWS account with `terraform apply` and removes it with `terraform destroy`, from a Lambda. A demo is therefore **a Terraform root module plus whatever it deploys**. In this repository that is a Go web service in an ECS task behind CloudFront, with Confluent Cloud resources made by the `confluent` provider.

The authoritative contract is `demos/launchpad/docs/specs/001_design.md` ("Demo interface", "Shutdown hook") and `demos/launchpad/README.md` ("Adding a demo"). The README links `002_design.md` and `001_overview.md`, which are not in the repository; the design is `001_design.md`. `demos/launchpad/backend/internal/demoif/demoif.go` is the check Launchpad actually runs, so when the docs and the code differ, the code wins.

Two finished demos are the reference. Copy their shape rather than inventing one:

| | `demos/automated-api-diagnosis` | `demos/pims` |
|---|---|---|
| Good model for | The simplest case: one container, nothing to undo on destroy | Several containers in one task, a shutdown hook |
| Design doc | `docs/specs/launchpad.md` | `docs/specs/launchpad.md` |
| Terraform | `tf/` (`confluent.tf`, `flink.tf`, `network.tf`, `cloudfront.tf`, `ecs.tf`, `secrets.tf`, `outputs.tf`, `variables.tf`, `versions.tf`, `tests/`) | same layout, plus `launchpad_shutdown` in `outputs.tf` |

## 1. Layout of a demo

```
demos/<id>/
  demo.json         {"name": "...", "description": "..."}   (optional, but always write it)
  tf/               Terraform. Required
  cmd/server/ …     the app (Go module, UI embedded in the binary)
  docker/Dockerfile the image
  docs/specs/launchpad.md   design: what runs where, setup order, shutdown, env contract, access
  CLAUDE.md, README.md
.github/workflows/<id>-image.yml   publishes the image (outside the demo dir)
```

* `<id>` is the directory name: `^[a-z0-9][a-z0-9-]{0,30}$`, so lower case, digits, hyphens, **31 characters at most**. It ends up in tags, state keys and AWS names.
* Launchpad only discovers a directory that has a `tf/` directory with `.tf` files.
* `demo.json` `name` ≤ 80 characters, `description` ≤ 500.
* Launchpad downloads `demos/<id>/` from GitHub **at a commit**, so a demo does not exist for Launchpad until it is committed and pushed to the branch the stack reads (`DemosRef`, default `main`). The catalog is cached for 10 minutes; an admin's refresh re-reads it. A retry of a failed launch reuses the cached commit, so after pushing a fix, **refresh the catalog and then retry**. Teardown uses the commit that was last applied.

## 2. The Terraform interface (what Launchpad checks)

`demoif.Check` reads only the **top-level** `tf/*.tf` files (subdirectories such as `tf/flink/` are extracted and usable, but are not scanned). It fails the demo, with `invalid_demo`, if:

* `variable "aws_region"` or `variable "launchpad_tags"` is missing.
* `output "launchpad_url"` is missing.
* a `backend "..." {` block exists. Launchpad supplies an S3 backend at `demos/<id>/terraform.tfstate`.
* only one of `confluent_cloud_api_key` / `confluent_cloud_api_secret` is declared. Declare both or neither.

Launchpad passes these as `TF_VAR_*`:

| Variable | Notes |
|---|---|
| `aws_region` | Where Launchpad runs. Use it for the AWS provider **and** the Confluent cluster, and check Confluent features (for example Flink: `confluent flink region list --cloud AWS`) exist there |
| `launchpad_tags` | `map(string)`. Put it in the AWS provider's `default_tags` so everything is tagged `launchpad:managed=true` and `launchpad:demo=<id>` |
| `launchpad_demo_id` | Optional. Give it a default equal to the directory name so `terraform validate` and tests work without Launchpad |
| `confluent_cloud_api_key`, `confluent_cloud_api_secret` | Optional, `sensitive`. Declaring **both** makes Launchpad require the user to have saved a key before launching, and it sets `CONFLUENT_CLOUD_API_KEY/SECRET` in the environment too. Teardown uses the launching user's key |

Outputs: `launchpad_url` (required string, empty allowed) and the optional `launchpad_shutdown` (section 5). Other outputs are free; the existing demos expose IDs and the log group for debugging.

Rules that follow from how the runner works:

* **Idempotent and repeatable.** A run is limited to 15 minutes per Lambda invocation, but Launchpad interrupts Terraform at the limit (SIGINT) and resumes it in a new invocation, up to 8 times. A slow `apply` is fine; a non-repeatable one is not. Never use `local-exec` that cannot be re-run.
* **No local tools.** The runner is a `provided.al2023` Lambda with Terraform downloaded to `/tmp` (the version Launchpad pins in `internal/terraform/terraform.go`; so keep `required_version = ">= 1.10.0"`, not an exact version). There is no Docker, shell tooling, `aws` CLI or Go toolchain for `local-exec` to rely on. Do everything with providers. Providers are downloaded on each run, so keep the set small (the demos use `aws`, `confluent`, `random`, and `time` where needed).
* **Everything must be destroyable.** `terraform destroy` runs with no human. No manual resources, and set deletion so nothing lingers: for example `recovery_window_in_days = 0` on Secrets Manager secrets, a VPC of the demo's own so nothing is left behind. Prefer no NAT gateway (cost): a public IP on the task, with the security group allowing only the load balancer in.
* **Resource names must not collide.** Confluent environment and service account names are unique per organisation, so build names from `"${var.launchpad_demo_id}-${random_id.suffix.hex}"`. Because Launchpad runs one instance per demo, AWS names collide only across Launchpads in one account; the suffix covers that too.
* **Secrets never in the task definition.** Anyone who can describe the task reads it. Put them in one Secrets Manager secret and inject them with the task's `secrets` (`<arn>:<KEY>::`). Mark outputs that carry secrets `sensitive`. Launchpad scrubs known secrets from the log tail, but do not rely on it.
* **IAM.** The runner's role defaults to `AdministratorAccess`, but the operator may narrow it with `DemoDeployPolicyArn`. Create only what the demo needs and keep IAM to the ECS execution role (see `tf/ecs.tf`); the app itself should get no AWS permissions unless it truly needs them.
* **Confluent ordering gotchas** (all learned in the existing demos; see their `tf` comments): API keys take ~30 s to become valid (`time_sleep`); register a topic's Avro schema **before** creating the topic when Flink will read it; the service account needs `EnvironmentAdmin` and, for Flink, `FlinkAdmin` plus `CloudClusterAdmin` on the cluster; Flink statements must `depends_on` the tables they read; delete Flink statements before the compute pool and the service before the cluster.

## 3. Running the app: the proven pattern

Follow `demos/automated-api-diagnosis/tf` (single container) or `demos/pims/tf` (sidecars):

```
Browser ─HTTPS─► CloudFront ─VPC origin─► internal ALB :80 ─► ECS Fargate task (app :8080)
```

* **Image.** Build it in GitHub Actions and publish to `ghcr.io/<owner>/<id>` (copy `.github/workflows/automated-api-diagnosis-image.yml`: path filter on `demos/<id>/**`, `linux/amd64` because Fargate runs `X86_64`, tags `sha`, branch, `latest`). Terraform takes `image_registry` and `image_tag` variables. GHCR packages start **private**: either make the package public once, or set `registry_credentials_secret_arn`. Workflow commits use the `repo:` prefix. Pin action versions by SHA as the existing workflows do.
* **HTTPS without a domain.** CloudFront on its own `*.cloudfront.net` certificate, no caching (`CachingDisabled`), the `AllViewerAndCloudFrontHeaders` origin request policy, `redirect-to-https`, `compress = false`. It reaches the **internal** ALB through `aws_cloudfront_vpc_origin`; the ALB admits only the `CloudFront-VPCOrigins-Service-SG` security group. Copy `cloudfront.tf` and `network.tf` almost verbatim. Launchpad calls the shutdown hook over HTTPS only, so this is not optional if the demo has a hook.
* **Long-lived streams.** CloudFront waits 60 s for an origin, and the ALB idle timeout is set to 120 s. A response that keeps sending is not idle, so for Server-Sent Events ping every ~15 s.
* **Health.** `GET /healthz` returns 200 with no authentication. The target group checks it, and `aws_ecs_service` uses `wait_for_steady_state = true`, so **Terraform waits until the app is healthy** and `launchpad_url` is only reported for a working demo. Add a deployment circuit breaker so a task that cannot start (such as a private image) fails the apply instead of waiting out the timeout.
* **One task.** `desired_count = 1`, `deployment_maximum_percent = 100`, `deployment_minimum_healthy_percent = 0`, if two copies would double traffic to a billed cluster.
* **Logs.** A CloudWatch log group (14 days) with `awslogs`; output its name.
* **Configuration contract.** The app reads its configuration from environment variables named `<PREFIX>_…` (secrets from Secrets Manager). Document them in a table in `docs/specs/launchpad.md` and **add a `contract_test.go`** that fails when the variables in `tf/ecs.tf` and the server's config disagree. Both demos have one. Redact secrets in the config's `String`/`LogValue`.
* **State is in memory.** A replaced task starts from defaults. If that matters, say so in the design doc as a known limit rather than adding storage.
* **Simulators start by themselves** (an `AUTOSTART` default of true) so the demo is meaningful when the user opens the link. A Flink anomaly detector with a five-minute baseline is the model: tell the user in the UI how long until something interesting happens.

## 4. Access: the UI is not open

A demo's UI usually controls something billed, so it is gated. Copy `internal/server/http.go` of either demo:

1. Terraform makes `random_password.access_token` (32 chars, `special = false`) and stores it in the secret.
2. `launchpad_url` = `https://<cloudfront domain>/?access=<token>`, marked `sensitive` (`terraform output -raw` still reads it).
3. The server accepts `?access=<token>` on a GET, sets a `Secure; HttpOnly` cookie derived from it (it knows the viewer used HTTPS from the `CloudFront-Forwarded-Proto` header) and redirects to the same URL **without** the token. Compare in constant time.
4. Everything else gets 401, except `/healthz` (the load balancer) and `POST /shutdown` (bearer token, if there is a hook).
5. State-changing requests also need a custom header (`X-APIDIAG`, `X-PIMS` style) as a CSRF guard, and the server bounds what the browser may set (rates, sizes) since the cluster is billed. Names and paths are not the browser's to set.

## 5. Shutdown hook: only if the app creates things Terraform does not own

If Terraform owns everything and the app creates nothing, **do not add a hook** (automated-api-diagnosis has none). Add one when the app changes things that block `terraform destroy`; the existing example is PIMS switching on Tableflow, which stops Terraform deleting the topics.

Declare a sensitive output holding JSON:

```hcl
output "launchpad_shutdown" {
  value     = jsonencode({ url = "https://${aws_cloudfront_distribution.demo.domain_name}/shutdown", token = random_password.access_token.result })
  sensitive = true
}
```

Before `destroy` (but not when resuming a later slice) the runner sends `POST <url>` with `Authorization: Bearer <token>`: no redirects, 65 s, `url` must be `https`. The app must answer:

| Answer | Runner does |
|---|---|
| any 2xx | destroys |
| 502, 503, 504 or no answer (3 tries, 5 s apart) | destroys. The app is gone, and a gone demo must still be destroyable |
| anything else | **does not destroy**; the run ends `teardown_failed` showing the app's message; the person retries |
| unusable output | logs and destroys |

So:

* Answer **500** with `{"error": "..."}` when you tried and failed. Never 502/503/504 for that: Launchpad would destroy anyway.
* Answer within about 50 s (CloudFront waits 60).
* The call is **idempotent** (it may be repeated after a failure). An already-undone thing counts as done.
* The bearer token is the access token; the cookie and the `?access=` form do not count.
* After shutdown the app must not start work again.
* Never delete anything Terraform owns from the app (PIMS returns `ErrManaged` in server mode). The app undoes only what it did itself, and Terraform must not manage those resources (PIMS's `contract_test.go` checks that no `confluent_tableflow_topic` is in `tf/`).

## 6. Build order for a new demo

1. **Design first.** Write `docs/specs/launchpad.md`: diagram of what runs where, setup order, what undoes what at shutdown, the env contract, access, image, regions, known limits. Both existing demos have exactly these sections.
2. **App**: server with `/healthz`, the access gate, config from env, a `contract_test.go`, then the UI embedded in the binary.
3. **Dockerfile** and the image workflow. Mind cgo (`confluent-kafka-go` needs a glibc build with `CGO_ENABLED=1`, as in automated-api-diagnosis' Dockerfile) and `.dockerignore` for `tf`, `docs`, `.git`.
4. **Terraform**: `versions.tf` (providers, `default_tags = var.launchpad_tags`), `variables.tf`, `confluent.tf`, `network.tf`, `cloudfront.tf`, `secrets.tf`, `ecs.tf`, `outputs.tf`. Put a header comment on each file saying what it is for and why.
5. **Terraform tests** in `tf/tests/*.tftest.hcl` with `mock_provider`s, checking the task definition: secrets are injected, ports and image are right, the service waits for the tables it reads.
6. **`demo.json`**, `README.md`, `CLAUDE.md` (copy the structure of the existing ones: what it is, commands, architecture, invariants, conventions).
7. **Verify locally, then push, refresh, launch** (below).

## 7. Verify

From `demos/<id>` (no cloud account needed for these):

```sh
go vet ./... && go test -race ./...
(cd frontend && npm ci && npm run build)
(cd tf && terraform init -backend=false && terraform validate && terraform test)
docker build -f docker/Dockerfile -t <id> .
```

Also check the interface the way Launchpad does: `cd demos/launchpad/backend && go test ./internal/demoif/...`, and read `demoif.Check` against `tf/*.tf` by eye if in doubt.

Then, to prove it works in the cloud: push, make the image package public (or give it credentials), open Launchpad, click **Refresh** (admin), **Launch**, open the URL, then **Tear down** and confirm no `launchpad:demo=<id>` resources remain (the Confluent environment and the AWS tag are the two places to look). Launch, tear down, launch again: the second launch proves repeatability.

## 8. Debugging a failed run

* The demo page in Launchpad shows the **log tail** of the run and the `error`. The runner logs are in the `runner` Lambda's CloudWatch group.
* `invalid_demo` → section 2's list. `credentials_required` → the demo declares the Confluent variables and the user has not saved a key.
* `launch_failed` then retry: the retry runs the **cached** commit. Refresh the catalog first.
* Apply stuck on the ECS service: the task cannot start or never passes `/healthz`. Check the log group the demo outputs; for a private GHCR image, the circuit breaker should have failed it with an image pull error.
* `teardown_failed` with a message from the app: the shutdown hook answered something other than 2xx/502/503/504. Fix the cause and retry teardown, which asks the hook again.
* `teardown_failed` otherwise: Terraform could not delete something. Common causes are a Confluent resource still in use (Tableflow on a topic, a Flink statement running on the pool) or a dependency order. Retry first, since many deletes are eventually consistent.
* A stale state lock after an interrupted run is cleared by the runner itself (`force-unlock`); do not clear it by hand.

## 9. Conventions in this repository

* Commit messages follow the `git-commit-message` skill: `<asset>: <type>(<scope>): <summary>`, where `<asset>` is the demo's directory name (`repo:` for the image workflow and anything outside a demo), and one commit per asset. Scopes the demos use: `server`, `frontend`, `tf`, `docker`.
* Review Go changes with the `go-code-reviewer` agent, and everything with the `security-reviewer` agent before a commit (`.claude/agents/`).
* Log with slog, sentence-case messages. Never log secrets, tokens or the Launchpad URL.
* Comments say why, as the existing Terraform does; keep the design doc and `CLAUDE.md` in step with the code in the same change.
