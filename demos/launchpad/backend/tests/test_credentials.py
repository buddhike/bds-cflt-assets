import pytest

from launchpad import credentials
from launchpad.errors import ApiError


def test_roundtrip(svc):
    out = credentials.save(svc, "ann@confluent.io", " KEY12345 ", "s3cret/+=")
    assert out == {"configured": True, "apiKeyHint": "2345", "updatedAt": out["updatedAt"]}
    got = credentials.load(svc, "ann@confluent.io")
    assert (got.api_key, got.api_secret) == ("KEY12345", "s3cret/+=")


def test_secret_is_not_stored_or_returned_in_clear(svc):
    credentials.save(svc, "ann@confluent.io", "KEY12345", "topsecretvalue")
    raw = svc.store.get("USER#ann@confluent.io", "CREDS")
    assert b"topsecretvalue" not in bytes(raw["ciphertext"].value)
    assert "topsecretvalue" not in str(credentials.status(svc, "ann@confluent.io"))


def test_blob_is_bound_to_its_owner(svc):
    credentials.save(svc, "ann@confluent.io", "KEY12345", "topsecretvalue")
    blob = svc.store.get("USER#ann@confluent.io", "CREDS")
    svc.store.put({**blob, "pk": "USER#bob@confluent.io"})
    with pytest.raises(Exception):
        credentials.load(svc, "bob@confluent.io")


def test_status_load_delete_when_none(svc):
    assert credentials.status(svc, "ann@confluent.io") == {"configured": False}
    assert credentials.load(svc, "ann@confluent.io") is None
    credentials.save(svc, "ann@confluent.io", "KEY12345", "x" * 10)
    credentials.delete(svc, "ann@confluent.io")
    assert credentials.load(svc, "ann@confluent.io") is None


@pytest.mark.parametrize("key,secret", [("", "x"), ("k", ""), ("k k", "x"), ("k", "x\ny"), (None, "x"), ("k", 3), ("k" * 201, "x")])
def test_invalid_input_is_rejected(svc, key, secret):
    with pytest.raises(ApiError) as e:
        credentials.save(svc, "ann@confluent.io", key, secret)
    assert e.value.status == 400


def test_repr_hides_the_secret():
    assert "hunter2" not in repr(credentials.Credentials("ABCDEFG", "hunter2"))
