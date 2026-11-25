# Launchpad

Launches demos into an AWS account and tears them down again. The design is in
[docs/specs/002_design.md](docs/specs/002_design.md), which implements
[001_overview.md](docs/specs/001_overview.md).

* Backend: Python on AWS Lambda, DynamoDB, SNS, Secrets Manager, KMS. See [backend/](backend).
* Front end: React. See [frontend/](frontend).
* Deployment: one CloudFormation template, [template.yaml](template.yaml).

## Deploy

You need the AWS CLI with credentials, Node.js and `zip`.

```sh
scripts/deploy.sh --stack-prefix acme
```

It prints the site URL and the dashboard URL. CloudFormation reads the Lambda
code from S3 before it creates anything, so the script first deploys a small
stack, `<prefix>-launchpad-artifacts` ([artifacts.yaml](artifacts.yaml)), whose bucket
holds the builds. `--stack-prefix` is required. It starts the name of every stack
the script makes (`acme-launchpad`), so more than one Launchpad can share an
account. Useful options:

| Option | Meaning |
|---|---|
| `--artifact-bucket my-bucket` | Stage the builds in an existing bucket in the same region, and skip the extra stack |
| `--param SuperUserEmail=you@example.com` | The one person who can sign in. Their address names their saved credentials and demos. The template has a default |
| `--param DemosRef=launchpad` | The branch demos are read from. Default `main` |
| `--param DemosPath=launchpad-demos` | The directory whose sub directories are demos. Default `demos` |
| `--param DemoDeployPolicyArn=arn:...` | Narrow what Terraform may create. Default is `AdministratorAccess` |

**The sign-in phone number.** Codes are texted, through SNS, to the number in
the Secrets Manager secret the stack creates (the `SignInSecretArn` output).
It holds a placeholder, and nobody can sign in until you replace it. The script
prints the command:

```sh
aws secretsmanager put-secret-value --secret-id <SignInSecretArn> --secret-string '+61400000000'
```

Use the E.164 form, with the `+` and the country code. Later deploys leave the
value alone, and the number can be changed at any time, with no deploy. The
application sends only to the number in the secret. The number a person types is
compared with it and is never a destination. While SNS's SMS sandbox is on, the number must be
verified there first (console: End User Messaging, SMS sandbox), or AWS must
grant production access.

**Private repository.** `export GITHUB_TOKEN=...` (read-only contents access)
before running the script.

## Use

1. Open the site and enter your phone number. If it is the number in the
   secret, a six digit code is texted to it, and you enter the code to reach the
   demos. The page says a code was sent whatever you type, so it never shows
   whether a number is the right one. A wrong number gets no code. If none comes
   when it should, look in the API function's log for `Sign-in code not sent`,
   which names the reason, such as `too_soon`, `sign_in_unavailable` or
   `sms_failed`, or at the `SignInNotSent` metric on the dashboard. A code works
   for 10 minutes and 5 guesses, and a new one replaces it.
2. Save your Confluent Cloud API key under **Confluent Cloud key** if the demo
   you want needs one.
3. **Launch** a demo. When it is running, **Open demo** goes to its address.
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
cd backend && python -m venv .venv && .venv/bin/pip install boto3 pytest 'moto[dynamodb,kms,sns,secretsmanager]'
.venv/bin/python -m pytest

cd frontend && npm ci && npm run typecheck && npm run build
cfn-lint template.yaml
```

`npm run dev` serves the app. To use a deployed stack's API, run
`LAUNCHPAD_API=https://<distribution>.cloudfront.net npm run dev`.
