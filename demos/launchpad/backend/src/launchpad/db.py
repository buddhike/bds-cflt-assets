"""Access to the single DynamoDB table."""

from decimal import Decimal
from typing import Any, Iterator

from botocore.exceptions import ClientError


class ConditionFailed(Exception):
    """A conditional write did not apply."""


def plain(value: Any) -> Any:
    """Turn DynamoDB's Decimals back into ints and floats."""
    if isinstance(value, Decimal):
        return int(value) if value == value.to_integral_value() else float(value)
    if isinstance(value, dict):
        return {k: plain(v) for k, v in value.items()}
    if isinstance(value, list):
        return [plain(v) for v in value]
    return value


class Store:
    def __init__(self, table):
        self._t = table

    def get(self, pk: str, sk: str) -> dict | None:
        item = self._t.get_item(Key={"pk": pk, "sk": sk}, ConsistentRead=True).get("Item")
        return plain(item) if item else None

    def put(self, item: dict, condition: str | None = None, names=None, values=None) -> None:
        kwargs: dict[str, Any] = {"Item": item}
        if condition:
            kwargs["ConditionExpression"] = condition
        if names:
            kwargs["ExpressionAttributeNames"] = names
        if values:
            kwargs["ExpressionAttributeValues"] = values
        try:
            self._t.put_item(**kwargs)
        except ClientError as e:
            if e.response["Error"]["Code"] == "ConditionalCheckFailedException":
                raise ConditionFailed() from e
            raise

    def update(
        self,
        pk: str,
        sk: str,
        expression: str,
        values: dict,
        names: dict | None = None,
        condition: str | None = None,
    ) -> dict:
        kwargs: dict[str, Any] = {
            "Key": {"pk": pk, "sk": sk},
            "UpdateExpression": expression,
            "ExpressionAttributeValues": values,
            "ReturnValues": "ALL_NEW",
        }
        if names:
            kwargs["ExpressionAttributeNames"] = names
        if condition:
            kwargs["ConditionExpression"] = condition
        try:
            return plain(self._t.update_item(**kwargs)["Attributes"])
        except ClientError as e:
            if e.response["Error"]["Code"] == "ConditionalCheckFailedException":
                raise ConditionFailed() from e
            raise

    def delete(self, pk: str, sk: str, condition: str | None = None, values: dict | None = None) -> None:
        kwargs: dict[str, Any] = {"Key": {"pk": pk, "sk": sk}}
        if condition:
            kwargs["ConditionExpression"] = condition
        if values:
            kwargs["ExpressionAttributeValues"] = values
        try:
            self._t.delete_item(**kwargs)
        except ClientError as e:
            if e.response["Error"]["Code"] == "ConditionalCheckFailedException":
                raise ConditionFailed() from e
            raise

    def query(self, pk: str, sk_prefix: str = "") -> Iterator[dict]:
        """Every item of a partition whose sort key starts with sk_prefix."""
        kwargs: dict[str, Any] = {"ConsistentRead": True}
        if sk_prefix:
            kwargs["KeyConditionExpression"] = "pk = :pk AND begins_with(sk, :sk)"
            kwargs["ExpressionAttributeValues"] = {":pk": pk, ":sk": sk_prefix}
        else:
            kwargs["KeyConditionExpression"] = "pk = :pk"
            kwargs["ExpressionAttributeValues"] = {":pk": pk}
        while True:
            page = self._t.query(**kwargs)
            for item in page["Items"]:
                yield plain(item)
            if "LastEvaluatedKey" not in page:
                return
            kwargs["ExclusiveStartKey"] = page["LastEvaluatedKey"]

    def query_index(self, gsi1pk: str) -> Iterator[dict]:
        """Items on the gsi1 index, oldest first."""
        kwargs: dict[str, Any] = {
            "IndexName": "gsi1",
            "KeyConditionExpression": "gsi1pk = :p",
            "ExpressionAttributeValues": {":p": gsi1pk},
        }
        while True:
            page = self._t.query(**kwargs)
            for item in page["Items"]:
                yield plain(item)
            if "LastEvaluatedKey" not in page:
                return
            kwargs["ExclusiveStartKey"] = page["LastEvaluatedKey"]
