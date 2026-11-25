"""The AWS clients and settings the code needs, built once per container."""

from dataclasses import dataclass
from typing import Any

import boto3

from .config import Settings
from .db import Store


@dataclass
class Services:
    settings: Settings
    store: Store
    kms: Any
    ses: Any
    lambda_client: Any
    # urlopen-like callable used for outbound HTTP, replaced in tests.
    http: Any = None

    @classmethod
    def from_env(cls) -> "Services":
        s = Settings.from_env()
        ddb = boto3.resource("dynamodb", region_name=s.region)
        return cls(
            settings=s,
            store=Store(ddb.Table(s.table_name)),
            kms=boto3.client("kms", region_name=s.region),
            ses=boto3.client("ses", region_name=s.region),
            lambda_client=boto3.client("lambda", region_name=s.region),
        )
