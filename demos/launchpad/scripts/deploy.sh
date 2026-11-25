#!/usr/bin/env bash
#
# Builds Launchpad and deploys it as a CloudFormation stack.
#
#   scripts/deploy.sh --artifact-bucket my-bucket --sender-email launchpad@example.com
#
# Options (all but the first two are optional):
#   --artifact-bucket NAME   An existing S3 bucket for the Lambda code and the web build.
#   --sender-email ADDRESS   Where sign-in codes come from. Must be a verified SES identity.
#   --stack NAME             Stack name. Default: launchpad
#   --region REGION          Default: the AWS CLI's region.
#   --param KEY=VALUE        Extra template parameter, repeatable. Example:
#                            --param DemosRef=launchpad --param CreateSesIdentity=false
#
# A GitHub token for a private repository is read from $GITHUB_TOKEN, not from an
# argument, so it does not show up in the process list.
#
# Needs: aws, zip, npm, a shasum or sha256sum.

set -euo pipefail

stack=launchpad
region=${AWS_REGION:-${AWS_DEFAULT_REGION:-}}
bucket=
sender=
params=()

while [[ $# -gt 0 ]]; do
  case $1 in
    --artifact-bucket) bucket=$2; shift 2 ;;
    --sender-email) sender=$2; shift 2 ;;
    --stack) stack=$2; shift 2 ;;
    --region) region=$2; shift 2 ;;
    --param) params+=("$2"); shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
[[ -n $bucket && -n $sender ]] || { echo "--artifact-bucket and --sender-email are required (see --help)" >&2; exit 2; }
[[ -n $region ]] || region=$(aws configure get region || true)
[[ -n $region ]] || { echo "no region: pass --region or set one for the AWS CLI" >&2; exit 2; }
export AWS_DEFAULT_REGION=$region

root=$(cd "$(dirname "$0")/.." && pwd)
build=$root/build
sha() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -c1-16; }

echo "==> Building the backend"
rm -rf "$build"
mkdir -p "$build/backend"
cp -R "$root/backend/src/launchpad" "$build/backend/launchpad"
find "$build/backend" -name __pycache__ -type d -prune -exec rm -rf {} +

echo "==> Building the web app"
(cd "$root/frontend" && npm ci --no-audit --no-fund && npm run build)
(cd "$root/frontend/dist" && zip -qr "$build/frontend.zip" .)
frontend_key=launchpad/frontend-$(sha "$build/frontend.zip").zip
aws s3 cp --only-show-errors "$build/frontend.zip" "s3://$bucket/$frontend_key"

echo "==> Packaging"
aws cloudformation package \
  --template-file "$root/template.yaml" \
  --s3-bucket "$bucket" --s3-prefix launchpad \
  --output-template-file "$build/packaged.yaml" >/dev/null

overrides=(
  "SenderEmail=$sender"
  "FrontendArtifactBucket=$bucket"
  "FrontendArtifactKey=$frontend_key"
)
[[ -z ${GITHUB_TOKEN:-} ]] || overrides+=("GitHubToken=$GITHUB_TOKEN")
overrides+=("${params[@]}")

echo "==> Deploying stack $stack in $region"
aws cloudformation deploy \
  --template-file "$build/packaged.yaml" \
  --stack-name "$stack" \
  --capabilities CAPABILITY_IAM \
  --tags launchpad:managed=true \
  --no-fail-on-empty-changeset \
  --parameter-overrides "${overrides[@]}"

echo
aws cloudformation describe-stacks --stack-name "$stack" \
  --query 'Stacks[0].Outputs[].[OutputKey,OutputValue]' --output text
echo
echo "If SES is in its sandbox, verify the address in the email AWS sent to $sender,"
echo "and verify each address that will sign in, or ask AWS to lift the sandbox."
