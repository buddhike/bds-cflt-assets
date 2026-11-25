# Launchpad

Launches demos into an AWS account and tears them down again. The design is in
[docs/specs/002_design.md](docs/specs/002_design.md), which implements
[001_overview.md](docs/specs/001_overview.md).

* Backend: Python on AWS Lambda, DynamoDB, SES, KMS. See [backend/](backend).
* Front end: React. See [frontend/](frontend).
* Deployment: one CloudFormation template, [template.yaml](template.yaml).

## Deploy

You need the AWS CLI with credentials, Node.js and `zip`.

```sh
scripts/deploy.sh --sender-email launchpad@example.com
```

It prints the site URL and the dashboard URL. CloudFormation reads the Lambda
code from S3 before it creates anything, so the script first deploys a small
stack, `launchpad-artifacts` ([artifacts.yaml](artifacts.yaml)), whose bucket
holds the builds. Useful options:

| Option | Meaning |
|---|---|
| `--artifact-bucket my-bucket` | Stage the builds in an existing bucket in the same region, and skip the extra stack |
| `--param SuperUserEmail=you@example.com` | Who becomes the super user on their first sign-in. The template has a default |
| `--param AllowedEmailDomain=example.com` | Only addresses at this domain can register. Default `confluent.io` |
| `--param DemosRef=launchpad` | The branch demos are read from. Default `main` |
| `--param DemosPath=launchpad-demos` | The directory whose sub directories are demos. Default `demos` |
| `--param CreateSesIdentity=false` | Use an address or domain already verified in SES |
| `--param DemoDeployPolicyArn=arn:...` | Narrow what Terraform may create. Default is `AdministratorAccess` |

**SES.** The stack creates an identity for the sender, and AWS emails that
address a link to confirm it. In the SES sandbox, every address that signs in
must be verified too. Ask AWS to lift the sandbox for a real roll-out.

**Private repository.** `export GITHUB_TOKEN=...` (read-only contents access)
before running the script.

## Use

1. Open the site and enter your work address. A six digit code is emailed to
   you.
2. The first verified sign-in of the configured super user address is
   approved at once. Anyone else sees "request received" until a super user
   approves them under **Access requests**.
3. Save your Confluent Cloud API key under **Confluent Cloud key** if the demo
   you want needs one.
4. **Launch** a demo. When it is running, **Open demo** goes to its address.
   **Tear down** deletes everything it created.

Everything Launchpad creates carries the tag `launchpad:managed=true`.
Everything a demo creates is also tagged `launchpad:demo=<id>`.

## Adding a demo

A demo is a directory under the demos path with a `tf` directory:

```
demos/<id>/
  demo.json    optional: {"name": "...", "description": "..."}
  tf/          Terraform. Must follow the interface below
```

`<id>` is lower case letters, digits and hyphens, at most 31 characters.

Launchpad sets these Terraform variables, so declare the ones you use:

| Variable | Required | |
|---|---|---|
| `aws_region` | yes | The region Launchpad runs in |
| `launchpad_tags` | yes | Map of tags. Put it in the AWS provider's `default_tags` |
| `launchpad_demo_id` | no | The directory name |
| `confluent_cloud_api_key`, `confluent_cloud_api_secret` | no | The launching user's key. Declare both, and Launchpad makes the user save one before launching. The Confluent provider also reads them from its environment variables |

And it reads this output:

| Output | Required | |
|---|---|---|
| `launchpad_url` | yes | Where the user goes to use the demo. An empty string is allowed |

Do not declare a `backend`. Launchpad uses an S3 backend and keeps state at
`demos/<id>/terraform.tfstate` in the state bucket. Runs are limited to 15
minutes per Lambda invocation, but Launchpad resumes them, so a slow `apply` is
fine. Make sure `apply` and `destroy` are safe to repeat.

## Develop

```sh
cd backend && python -m venv .venv && .venv/bin/pip install boto3 pytest 'moto[dynamodb,kms,ses]'
.venv/bin/python -m pytest

cd frontend && npm ci && npm run typecheck && npm run build
cfn-lint template.yaml
```

`npm run dev` serves the app. To use a deployed stack's API, run
`LAUNCHPAD_API=https://<distribution>.cloudfront.net npm run dev`.
