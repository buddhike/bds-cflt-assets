import json
import stat
import time

import pytest

from launchpad import catalog, credentials, runner, runs, terraform

from fakes import CC_TF, GOOD_TF, SHA, FakeGitHub, Resp, tar_gz

FAKE_TF = r'''#!/bin/bash
FAKE_LOG=@LOG@; FAKE_LOCK=@LOCK@; FAKE_MODE=@MODE@
echo "terraform $@" >> "$FAKE_LOG"
case "$1" in
  init) exit 0 ;;
  output) echo "http://demo.example"; exit 0 ;;
  force-unlock) rm -f "$FAKE_LOCK"; exit 0 ;;
  apply|destroy)
    if [ -f "$FAKE_LOCK" ]; then
      echo "Error: Error acquiring the state lock"; echo "Lock Info:"; echo "  ID:        6f1c1c1e-1111-2222-3333-444455556666"; exit 1
    fi
    case "$FAKE_MODE" in
      ok) echo "Apply complete. key=$TF_VAR_confluent_cloud_api_key secret=$TF_VAR_confluent_cloud_api_secret"; exit 0 ;;
      fail) echo "Error: boom"; exit 1 ;;
      slow) trap 'echo Interrupt received; exit 1' INT; while true; do sleep 0.1; done ;;
    esac ;;
esac
'''


def set_mode(tmp_path, mode):
    """Install the fake terraform. The runner hides the test's environment from it, so the settings are baked in."""
    binary = tmp_path / "work" / "bin" / "terraform"
    binary.write_text(
        FAKE_TF.replace("@LOG@", str(tmp_path / "calls.log")).replace("@LOCK@", str(tmp_path / "lock")).replace("@MODE@", mode)
    )
    binary.chmod(binary.stat().st_mode | stat.S_IEXEC)


@pytest.fixture
def env(svc, tmp_path, monkeypatch):
    monkeypatch.setattr(runner, "WORK", tmp_path / "work")
    (tmp_path / "work" / "bin").mkdir(parents=True)
    set_mode(tmp_path, "ok")

    gh = FakeGitHub()
    gh.add_repo({"demos/pims/tf/main.tf": CC_TF, "demos/plain/tf/main.tf": GOOD_TF})

    def serve(req, timeout=None):
        if "codeload" in req.full_url:
            return Resp(tar_gz([
                (f"r-{SHA}/demos/pims/tf/main.tf", CC_TF.encode(), "file"),
                (f"r-{SHA}/demos/plain/tf/main.tf", GOOD_TF.encode(), "file"),
            ]))
        return gh(req, timeout)

    svc.http = serve
    catalog.refresh(svc)
    invoked = []
    monkeypatch.setattr(svc.lambda_client, "invoke", lambda **kw: invoked.append(json.loads(kw["Payload"])) or {})
    svc.invoked = invoked
    svc.calls = lambda: (tmp_path / "calls.log").read_text().splitlines() if (tmp_path / "calls.log").exists() else []
    svc.tmp = tmp_path
    return svc


def launch(svc, demo="plain", email="ann@confluent.io"):
    if demo == "pims":
        credentials.save(svc, email, "KEY12345", "hunter2hunter2")
    runs.request_launch(svc, demo, email)
    svc.invoked.clear()
    return {"demoId": demo, "runId": runs.get_raw(svc, demo)["runId"], "resumes": 0}


def test_successful_launch_reads_the_url(env):
    ev = launch(env)
    assert runner.handler_run(env, ev, lambda: 900) == "running"
    st = runs.public(runs.get_raw(env, "plain"))
    assert st["status"] == "running" and st["url"] == "http://demo.example"
    calls = env.calls()
    assert calls[0].startswith("terraform init") and "-backend-config=key=demos/plain/terraform.tfstate" in calls[0]
    assert any(c.startswith("terraform apply -auto-approve") for c in calls)
    backend = (runner.WORK / "src/plain/tf/zz_launchpad_backend.tf").read_text()
    assert 'backend "s3"' in backend


def test_credentials_reach_terraform_but_never_the_log(env):
    ev = launch(env, "pims")
    runner.handler_run(env, ev, lambda: 900)
    tail = runs.get_raw(env, "pims")["logTail"]
    assert "hunter2hunter2" not in tail and "KEY12345" not in tail and "secret=***" in tail


def test_environment_for_terraform_is_minimal(env, monkeypatch):
    monkeypatch.setenv("GITHUB_TOKEN", "ghp_leak")
    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "AKIA")
    e = runner.build_env(env, "pims", credentials.Credentials("K", "S"))
    assert "GITHUB_TOKEN" not in e and e["AWS_ACCESS_KEY_ID"] == "AKIA"
    assert json.loads(e["TF_VAR_launchpad_tags"]) == {"launchpad:managed": "true", "launchpad:demo": "pims"}
    assert e["TF_VAR_aws_region"] == "us-east-1" and e["CONFLUENT_CLOUD_API_KEY"] == "K"


def test_failed_apply_records_the_error(env, monkeypatch):
    set_mode(env.tmp, "fail")
    ev = launch(env)
    assert runner.handler_run(env, ev, lambda: 900) == "failed"
    st = runs.public(runs.get_raw(env, "plain"))
    assert st["status"] == "launch_failed" and "terraform apply failed" in st["error"] and "Error: boom" in st["logTail"]


def test_teardown_removes_the_state(env):
    ev = launch(env)
    runner.handler_run(env, ev, lambda: 900)
    runs.request_teardown(env, "plain", {"email": "ann@confluent.io", "role": "user"})
    ev = {"demoId": "plain", "runId": runs.get_raw(env, "plain")["runId"], "resumes": 0}
    assert runner.handler_run(env, ev, lambda: 900) == "destroyed"
    assert runs.get_raw(env, "plain") is None
    assert any(c.startswith("terraform destroy -auto-approve") for c in env.calls())


def test_stale_invocation_does_nothing(env):
    ev = launch(env)
    assert runner.handler_run(env, {**ev, "runId": "old"}, lambda: 900) == "stale"
    assert env.calls() == []


def test_a_run_that_runs_out_of_time_resumes_in_a_new_invocation(env, monkeypatch):
    set_mode(env.tmp, "slow")
    monkeypatch.setattr(runner, "RESERVE_SECONDS", 0)
    monkeypatch.setattr(runner, "MIN_USEFUL_SECONDS", 0)
    monkeypatch.setattr(terraform, "STOP_GRACE_SECONDS", 5)
    ev = launch(env)
    start = time.time()
    assert runner.handler_run(env, ev, lambda: 1.0) == "resumed"
    assert time.time() - start < 30
    assert env.invoked == [{"demoId": "plain", "runId": ev["runId"], "resumes": 1}]
    st = runs.get_raw(env, "plain")
    assert st["status"] == "launching" and st["resumes"] == 1
    assert "continuing in a new invocation" in st["logTail"]


def test_gives_up_after_too_many_resumes(env, monkeypatch):
    set_mode(env.tmp, "slow")
    monkeypatch.setattr(runner, "RESERVE_SECONDS", 0)
    monkeypatch.setattr(runner, "MIN_USEFUL_SECONDS", 0)
    ev = launch(env)
    ev["resumes"] = runner.MAX_RESUMES
    assert runner.handler_run(env, ev, lambda: 1.0) == "failed"
    assert runs.get_raw(env, "plain")["status"] == "launch_failed"
    assert env.invoked == []


def test_too_little_time_left_resumes_without_running_apply(env, monkeypatch):
    ev = launch(env)
    assert runner.handler_run(env, ev, lambda: 100) == "resumed"
    assert not any(c.startswith("terraform apply") for c in env.calls())


def test_stale_lock_from_a_cut_off_slice_is_cleared(env):
    (env.tmp / "lock").write_text("held")
    ev = launch(env)
    assert runner.handler_run(env, ev, lambda: 900) == "running"
    calls = env.calls()
    assert any(c.startswith("terraform force-unlock -force 6f1c1c1e") for c in calls)
    assert sum(c.startswith("terraform apply") for c in calls) == 2


def test_missing_credentials_fail_the_run_clearly(env):
    ev = launch(env, "pims")
    credentials.delete(env, "ann@confluent.io")
    assert runner.handler_run(env, ev, lambda: 900) == "failed"
    assert "credentials are gone" in runs.get_raw(env, "pims")["error"]
    assert env.calls() == []
