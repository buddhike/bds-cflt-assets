# Launchpad design

Implements [001_overview.md](001_overview.md). This is the contract the
segments of the build follow.

## Decisions that interpret the overview

| Topic | Decision | Why |
|---|---|---|
| Deployment tool | Terraform. The CloudFormation wording in the overview (lines 8-10) is superseded by lines 20-22. | The later lines are specific and the PIMS demo is Terraform. |
| Where demos live | `DemosPath` stack parameter, default `demos`. A demo is a direct sub directory that has a `tf/` directory. | PIMS lives in `demos/pims`. Set `DemosPath=launchpad-demos` to follow the overview literally. |
| One instance per demo | A demo has one deployment, shared by every approved user. | Terraform state is per demo, not per user. |
| Sign up and sign in | One passwordless flow: email, then OTP. | The overview has OTP only. The first verified OTP creates the user. |
| Credentials store | KMS encrypted blob in DynamoDB. | The overview says DynamoDB for storage. KMS keeps the secret off the page and out of backups in plain text. |
| Terraform compute | Lambda, container-less. The binary is downloaded to `/tmp`, checksum pinned. A run that hits the Lambda time limit is interrupted and resumed by a new invocation. | The overview says Lambda for compute. Terraform is idempotent, so resuming is safe. |
| Front end hosting | S3 + CloudFront. `/api/*` goes to an HTTP API. | Serverless, one origin, so no CORS. |

## Architecture

```
Browser ── CloudFront ──┬── S3 (React build)
                        └── /api/* ── HTTP API ── Lambda api ──┬── DynamoDB (single table)
                                                               ├── SES (OTP email)
                                                               ├── KMS (credentials)
                                                               └── async invoke ── Lambda runner
                                                                                     ├── GitHub (demo source, read only)
                                                                                     ├── S3 (terraform state)
                                                                                     └── terraform → the demo's resources
```

Everything Launchpad creates carries the tag `launchpad:managed=true`.
Everything a demo creates carries `launchpad:managed=true` and
`launchpad:demo=<id>`.

## DynamoDB (single table `pk`/`sk`)

| pk | sk | Attributes |
|---|---|---|
| `USER#<email>` | `PROFILE` | `status` (`pending`/`approved`/`denied`), `role` (`admin`/`user`), `createdAt`, `decidedBy`, `decidedAt`, `gsi1pk`=`STATUS#<status>`, `gsi1sk`=`createdAt` |
| `USER#<email>` | `CREDS` | `ciphertext` (KMS), `apiKeyHint` (last 4), `updatedAt` |
| `OTP#<email>` | `OTP` | `salt`, `hash`, `attempts`, `sentAt`, `hourStart`, `hourCount`, `ttl` |
| `SESSION#<sha256 token>` | `SESSION` | `email`, `ttl` |
| `CATALOG` | `DEMO#<id>` | `name`, `description`, `path`, `ref`, `needsConfluent` |
| `CATALOG` | `META` | `refreshedAt`, `ref` |
| `DEMO#<id>` | `STATE` | `status`, `runId`, `ref`, `launchedBy`, `credsOwner`, `url`, `error`, `logTail`, `resumes`, `updatedAt` |

Time to live (`ttl`) is enabled on the table, so OTPs and sessions expire.
Reads of an expired item must still check `ttl` because deletion is lazy.

## Sign in

1. `POST /api/auth/start {email}`: the domain must be `AllowedEmailDomain`.
   Sends a 6 digit OTP by SES. The same `202` is returned whether or not the
   user exists. Limits: 30 s between sends, 10 per hour.
2. `POST /api/auth/verify {email, otp}`: the OTP is valid for 10 minutes and 5
   guesses. Stored as `HMAC-SHA256(salt, otp)`, compared in constant time.
   On success:
   * no user yet and email equals `SuperUserEmail`: create `approved` `admin`.
   * no user yet otherwise: create `pending` `user`.
   * a session is created only for `approved` users. A `pending` or `denied`
     user is told their state and gets no session.
3. The session is an opaque 32 byte token in an `HttpOnly; Secure;
   SameSite=Strict` cookie. Only its hash is stored. Mutating calls also
   require the `X-Launchpad: 1` header.

## HTTP API

| Method and path | Who | Does |
|---|---|---|
| `POST /api/auth/start`, `/api/auth/verify`, `/api/auth/logout` | anyone | sign in |
| `GET /api/me` | session | the user, and whether credentials are saved |
| `GET /api/admin/requests` | admin | pending users |
| `GET /api/admin/users` | admin | every user |
| `POST /api/admin/users/{email}/approve`, `/deny` | admin | decide |
| `GET/PUT/DELETE /api/credentials` | session | Confluent Cloud key. `GET` never returns the secret |
| `GET /api/demos` | session | catalog merged with each demo's state |
| `POST /api/demos/refresh` | admin | re-read the repository now |
| `GET /api/demos/{id}` | session | one demo with its log tail |
| `POST /api/demos/{id}/launch` | session | start `terraform apply` |
| `POST /api/demos/{id}/teardown` | launcher or admin | start `terraform destroy` |

## Demo state machine

`status` is absent (not deployed), `launching`, `running`, `launch_failed`,
`tearing_down` or `teardown_failed`. Transitions are conditional writes, so
two clicks cannot start two runs:

* launch: from absent or `launch_failed` to `launching`
* teardown: from `running`, `launch_failed` or `teardown_failed` to
  `tearing_down`
* the runner moves `launching` to `running` or `launch_failed`, and
  `tearing_down` to absent or `teardown_failed`

`runId` is a fresh id per request. A runner whose `runId` no longer matches
the item exits, so a stale invocation never changes a newer run.

## Runner

1. Load the `STATE` item and check `runId`.
2. Download Terraform (once per container) and verify its SHA-256.
3. Download the demo source at the `ref` recorded at launch (the commit SHA of
   `DemosRepoRef` at that time), so teardown uses the code that was applied.
4. Check the interface below, write the backend file, `terraform init`.
5. `apply -auto-approve` or `destroy -auto-approve`, with a deadline. At the
   deadline Terraform gets SIGINT and 90 s to stop. If it has not finished
   the runner re-invokes itself with `resumes + 1`, up to 8 times.
6. After an interrupted run the state lock may be stale. The runner clears it
   with `force-unlock`, which is safe because the `runId` proves no other run
   of this demo is active.
7. On success `launchpad_url` is read with `terraform output`.

Secrets reach Terraform only as process environment (`TF_VAR_*`, and
`CONFLUENT_CLOUD_API_KEY`/`_SECRET`). The log tail is scrubbed of them.

## Demo interface

A demo directory has `tf/` and may have `demo.json` with `name` and
`description`. Launchpad passes (as `TF_VAR_*`):

| Variable | Type | Required | Value |
|---|---|---|---|
| `aws_region` | string | yes | the region Launchpad is deployed in |
| `launchpad_tags` | map(string) | yes | apply to every resource that takes tags, for example as provider `default_tags` |
| `launchpad_demo_id` | string | no | the directory name |
| `confluent_cloud_api_key`, `confluent_cloud_api_secret` | string, sensitive | no | the launching user's saved key. A demo that declares them needs one |

The demo must declare the output `launchpad_url` (string, the URL the user
opens, empty if there is none). It must not declare a `backend`: Launchpad
supplies an S3 backend at `demos/<id>/terraform.tfstate`.

## Observability

Both Lambdas emit CloudWatch Embedded Metric Format lines in the `Launchpad`
namespace (`SignInStarted`, `SignInFailed`, `UserRegistered`,
`DemoLaunchRequested`, `DemoLaunchSucceeded`, `DemoLaunchFailed`,
`DemoTeardownRequested`, `TerraformSeconds`). The CloudFormation template
creates one dashboard with those, plus Lambda, HTTP API, DynamoDB, SES and
CloudFront health.

## Build order

1. Backend foundations: config, table access, metrics, tags, tests.
2. Auth and users.
3. Credentials.
4. Catalog and demo source.
5. Runs and the runner.
6. API handler.
7. CloudFormation template and deploy script.
8. React front end.
9. PIMS Launchpad demo (see `demos/pims/docs/specs/launchpad.md`).
