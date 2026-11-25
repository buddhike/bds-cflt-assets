import pytest

from launchpad.db import ConditionFailed


def test_put_get_roundtrip_returns_plain_numbers(svc):
    svc.store.put({"pk": "A", "sk": "B", "n": 5})
    item = svc.store.get("A", "B")
    assert item["n"] == 5 and isinstance(item["n"], int)


def test_put_condition_blocks_overwrite(svc):
    svc.store.put({"pk": "A", "sk": "B"})
    with pytest.raises(ConditionFailed):
        svc.store.put({"pk": "A", "sk": "B"}, condition="attribute_not_exists(pk)")


def test_update_condition(svc):
    svc.store.put({"pk": "A", "sk": "B", "n": 1})
    got = svc.store.update("A", "B", "SET n = n + :one", {":one": 1, ":max": 2}, condition="n < :max")
    assert got["n"] == 2
    with pytest.raises(ConditionFailed):
        svc.store.update("A", "B", "SET n = n + :one", {":one": 1, ":max": 2}, condition="n < :max")


def test_query_prefix_and_index(svc):
    svc.store.put({"pk": "P", "sk": "DEMO#a"})
    svc.store.put({"pk": "P", "sk": "DEMO#b"})
    svc.store.put({"pk": "P", "sk": "META"})
    assert [i["sk"] for i in svc.store.query("P", "DEMO#")] == ["DEMO#a", "DEMO#b"]
    svc.store.put({"pk": "U1", "sk": "PROFILE", "gsi1pk": "STATUS#pending", "gsi1sk": 2})
    svc.store.put({"pk": "U2", "sk": "PROFILE", "gsi1pk": "STATUS#pending", "gsi1sk": 1})
    assert [i["pk"] for i in svc.store.query_index("STATUS#pending")] == ["U2", "U1"]


def test_delete_condition(svc):
    svc.store.put({"pk": "A", "sk": "B", "v": 1})
    with pytest.raises(ConditionFailed):
        svc.store.delete("A", "B", condition="v = :v", values={":v": 2})
    svc.store.delete("A", "B", condition="v = :v", values={":v": 1})
    assert svc.store.get("A", "B") is None
