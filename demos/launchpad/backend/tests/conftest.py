import os
import sys
from pathlib import Path

import boto3
import pytest
from moto import mock_aws

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from launchpad import clock  # noqa: E402
from launchpad.config import Settings  # noqa: E402
from launchpad.db import Store  # noqa: E402
from launchpad.services import Services  # noqa: E402

REGION = "us-east-1"
OWNER_PHONE = "+61400000001"


@pytest.fixture(autouse=True)
def aws_env(monkeypatch):
    for k, v in {
        "AWS_ACCESS_KEY_ID": "test",
        "AWS_SECRET_ACCESS_KEY": "test",
        "AWS_DEFAULT_REGION": REGION,
    }.items():
        monkeypatch.setenv(k, v)


@pytest.fixture
def now(monkeypatch):
    """A clock a test can move: `now.t += 60`."""

    class Clock:
        t = 1_700_000_000

    monkeypatch.setattr(clock, "now", lambda: Clock.t)
    return Clock


@pytest.fixture
def svc(now):
    with mock_aws():
        ddb = boto3.resource("dynamodb", region_name=REGION)
        table = ddb.create_table(
            TableName="launchpad",
            BillingMode="PAY_PER_REQUEST",
            KeySchema=[{"AttributeName": "pk", "KeyType": "HASH"}, {"AttributeName": "sk", "KeyType": "RANGE"}],
            AttributeDefinitions=[
                {"AttributeName": "pk", "AttributeType": "S"},
                {"AttributeName": "sk", "AttributeType": "S"},
                {"AttributeName": "gsi1pk", "AttributeType": "S"},
                {"AttributeName": "gsi1sk", "AttributeType": "N"},
            ],
            GlobalSecondaryIndexes=[
                {
                    "IndexName": "gsi1",
                    "KeySchema": [
                        {"AttributeName": "gsi1pk", "KeyType": "HASH"},
                        {"AttributeName": "gsi1sk", "KeyType": "RANGE"},
                    ],
                    "Projection": {"ProjectionType": "ALL"},
                }
            ],
        )
        kms = boto3.client("kms", region_name=REGION)
        key_id = kms.create_key()["KeyMetadata"]["KeyId"]
        secrets = boto3.client("secretsmanager", region_name=REGION)
        sign_in_secret = secrets.create_secret(Name="launchpad-sign-in", SecretString=OWNER_PHONE)["ARN"]
        settings = Settings(
            table_name="launchpad",
            kms_key_id=key_id,
            state_bucket="state-bucket",
            region=REGION,
            super_user_email="boss@confluent.io",
            demos_repo="owner/repo",
            demos_path="demos",
            demos_ref="main",
            github_token="",
            runner_function="launchpad-runner",
            sign_in_secret=sign_in_secret,
        )
        yield Services(
            settings=settings,
            store=Store(table),
            kms=kms,
            sns=boto3.client("sns", region_name=REGION),
            secrets=secrets,
            lambda_client=boto3.client("lambda", region_name=REGION),
        )
