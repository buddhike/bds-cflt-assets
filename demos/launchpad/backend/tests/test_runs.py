import json

import pytest

from launchpad import catalog, credentials, runs
from launchpad.errors import ApiError

from fakes import CC_TF, GOOD_TF, SHA, FakeGitHub

ANN = {"email": "ann@confluent.io", "role": "user"}
BOSS = {"email": "boss@confluent.io", "role": "admin"}
BOB = {"email": "bob@confluent.io", "role": "user"}


@pytest.fixture
def invoked(svc, monkeypatch):
    calls = []

    def fake_invoke(**kw):
        calls.append({**kw, "Payload": json.loads(kw["Payload"])})
        return {}

    monkeypatch.setattr(svc.lambda_client, "invoke", fake_invoke)
    return calls


@pytest.fixture
def cat(svc):
    gh = FakeGitHub()
    gh.add_repo({"demos/pims/tf/main.tf": CC_TF, "demos/plain/tf/main.tf": GOOD_TF, "demos/bad/tf/main.tf": ""})
    svc.http = gh
    catalog.refresh(svc)


def with_creds(svc, email="ann@confluent.io"):
    credentials.save(svc, email, "KEY12345", "secretsecret")


def test_launch_records_state_and_starts_the_runner(svc, cat, invoked):
    with_creds(svc)
    out = runs.request_launch(svc, "pims", "ann@confluent.io")
    assert out["status"] == "launching" and out["launchedBy"] == "ann@confluent.io" and out["ref"] == SHA
    (call,) = invoked
    assert call["InvocationType"] == "Event" and call["FunctionName"] == "launchpad-runner"
    assert call["Payload"]["demoId"] == "pims" and call["Payload"]["runId"] == runs.get_raw(svc, "pims")["runId"]


def test_launch_needs_credentials_only_when_the_demo_does(svc, cat, invoked):
    with pytest.raises(ApiError) as e:
        runs.request_launch(svc, "pims", "ann@confluent.io")
    assert e.value.code == "credentials_required" and invoked == []
    assert runs.request_launch(svc, "plain", "ann@confluent.io")["status"] == "launching"


def test_unknown_and_invalid_demos(svc, cat, invoked):
    with pytest.raises(ApiError) as e:
        runs.request_launch(svc, "nope", "ann@confluent.io")
    assert e.value.status == 404
    with pytest.raises(ApiError) as e:
        runs.request_launch(svc, "bad", "ann@confluent.io")
    assert e.value.code == "invalid_demo"


def test_second_launch_is_refused_while_running(svc, cat, invoked):
    runs.request_launch(svc, "plain", "ann@confluent.io")
    with pytest.raises(ApiError) as e:
        runs.request_launch(svc, "plain", "bob@confluent.io")
    assert e.value.code == "not_launchable" and len(invoked) == 1


def test_failed_launch_can_be_retried(svc, cat, invoked):
    runs.request_launch(svc, "plain", "ann@confluent.io")
    run = runs.get_raw(svc, "plain")["runId"]
    assert runs.finish(svc, "plain", run, runs.LAUNCH_FAILED, error="boom")
    assert runs.public(runs.get_raw(svc, "plain"))["error"] == "boom"
    runs.request_launch(svc, "plain", "bob@confluent.io")
    assert runs.get_raw(svc, "plain")["launchedBy"] == "bob@confluent.io"


def test_runner_that_cannot_start_leaves_a_failed_launch(svc, cat, monkeypatch):
    monkeypatch.setattr(svc.lambda_client, "invoke", lambda **kw: (_ for _ in ()).throw(RuntimeError("no")))
    with pytest.raises(ApiError) as e:
        runs.request_launch(svc, "plain", "ann@confluent.io")
    assert e.value.status == 502
    assert runs.get_raw(svc, "plain")["status"] == "launch_failed"


def launched(svc, demo="plain", email="ann@confluent.io"):
    runs.request_launch(svc, demo, email)
    run = runs.get_raw(svc, demo)["runId"]
    runs.finish(svc, demo, run, runs.RUNNING, url="http://x")
    return run


def test_teardown_by_launcher_and_by_admin_but_not_by_others(svc, cat, invoked):
    launched(svc)
    with pytest.raises(ApiError) as e:
        runs.request_teardown(svc, "plain", BOB)
    assert e.value.status == 403
    out = runs.request_teardown(svc, "plain", BOSS)
    assert out["status"] == "tearing_down"
    assert invoked[-1]["Payload"]["runId"] == runs.get_raw(svc, "plain")["runId"]


def test_teardown_conditions(svc, cat, invoked):
    with pytest.raises(ApiError) as e:
        runs.request_teardown(svc, "plain", ANN)
    assert e.value.status == 404
    runs.request_launch(svc, "plain", "ann@confluent.io")  # still launching
    with pytest.raises(ApiError) as e:
        runs.request_teardown(svc, "plain", ANN)
    assert e.value.code == "not_tearable"


def test_teardown_uses_the_launchers_credentials_or_falls_back_to_the_requester(svc, cat, invoked):
    with_creds(svc, "ann@confluent.io")
    launched(svc, "pims")
    runs.request_teardown(svc, "pims", BOSS)  # boss has none, ann still does
    assert runs.get_raw(svc, "pims")["credsOwner"] == "ann@confluent.io"
    run = runs.get_raw(svc, "pims")["runId"]
    runs.finish(svc, "pims", run, runs.TEARDOWN_FAILED, error="x")
    credentials.delete(svc, "ann@confluent.io")
    with pytest.raises(ApiError) as e:
        runs.request_teardown(svc, "pims", BOSS)
    assert e.value.code == "credentials_required"
    with_creds(svc, "boss@confluent.io")
    runs.request_teardown(svc, "pims", BOSS)
    assert runs.get_raw(svc, "pims")["credsOwner"] == "boss@confluent.io"


def test_stale_runner_cannot_change_a_newer_run(svc, cat, invoked):
    runs.request_launch(svc, "plain", "ann@confluent.io")
    old = runs.get_raw(svc, "plain")["runId"]
    runs.finish(svc, "plain", old, runs.LAUNCH_FAILED, error="x")
    runs.request_launch(svc, "plain", "ann@confluent.io")
    assert not runs.finish(svc, "plain", old, runs.RUNNING, url="http://stale")
    assert not runs.update_log(svc, "plain", old, "stale log")
    assert runs.get_raw(svc, "plain")["status"] == "launching"


def test_finishing_a_teardown_removes_the_state(svc, cat, invoked):
    launched(svc)
    runs.request_teardown(svc, "plain", ANN)
    run = runs.get_raw(svc, "plain")["runId"]
    assert runs.finish(svc, "plain", run, None)
    assert runs.get_raw(svc, "plain") is None
    assert runs.states_for(svc, ["plain", "pims"]) == {}


def test_public_view_hides_internals(svc, cat, invoked):
    runs.request_launch(svc, "plain", "ann@confluent.io")
    assert set(runs.public(runs.get_raw(svc, "plain"))) == {"status", "url", "error", "logTail", "launchedBy", "updatedAt", "ref"}
