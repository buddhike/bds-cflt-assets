"""The runner: applies or destroys one demo with Terraform.

It is invoked asynchronously by the API. A Lambda cannot run longer than 15
minutes and a demo can take longer than that to create, so a run that is not
finished near the limit tells Terraform to stop, then invokes the runner again.
Terraform picks up where the state says it was.
"""

import json
import logging
import os
import re
import time
from pathlib import Path
from typing import Callable

from . import credentials, interface, metrics, runs, source, tags, terraform
from .services import Services

log = logging.getLogger(__name__)

WORK = Path(os.environ.get("LAUNCHPAD_WORK_DIR", "/tmp/launchpad"))
MAX_RESUMES = 8
# Left for Terraform to stop (STOP_GRACE_SECONDS) and for the runner to write its result.
RESERVE_SECONDS = terraform.STOP_GRACE_SECONDS + 45
# Below this much time left there is no point starting Terraform.
MIN_USEFUL_SECONDS = 120

_LOCK_ID = re.compile(r"^\s*ID:\s+([0-9a-fA-F-]{36})\s*$", re.M)
# Environment that Terraform and its providers may see. Nothing else of the Lambda's is passed on.
_PASS_THROUGH = ("PATH", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_DEFAULT_REGION")


def backend_args(svc: Services, demo_id: str) -> list[str]:
    return [
        f"-backend-config=bucket={svc.settings.state_bucket}",
        f"-backend-config=key=demos/{demo_id}/terraform.tfstate",
        f"-backend-config=region={svc.settings.region}",
        "-backend-config=use_lockfile=true",
        "-backend-config=encrypt=true",
    ]


def build_env(svc: Services, demo_id: str, creds: credentials.Credentials | None) -> dict[str, str]:
    env = {k: os.environ[k] for k in _PASS_THROUGH if k in os.environ}
    env.update(
        {
            "HOME": str(WORK / "home"),
            "TF_IN_AUTOMATION": "1",
            "TF_INPUT": "0",
            "TF_CLI_ARGS": "-no-color",
            "TF_DATA_DIR": str(WORK / "data" / demo_id),
            "TF_PLUGIN_CACHE_DIR": str(WORK / "plugin-cache"),
            "TF_VAR_aws_region": svc.settings.region,
            "TF_VAR_launchpad_demo_id": demo_id,
            "TF_VAR_launchpad_tags": json.dumps(tags.demo_tags(demo_id)),
        }
    )
    if creds:
        env.update(
            {
                "TF_VAR_confluent_cloud_api_key": creds.api_key,
                "TF_VAR_confluent_cloud_api_secret": creds.api_secret,
                # The Confluent provider reads these itself.
                "CONFLUENT_CLOUD_API_KEY": creds.api_key,
                "CONFLUENT_CLOUD_API_SECRET": creds.api_secret,
            }
        )
    return env


def handler_run(svc: Services, event: dict, remaining_seconds: Callable[[], float]) -> str:
    """Do one slice of a run. Returns what happened, for the logs and for tests."""
    demo_id, run_id, resumes = event["demoId"], event["runId"], int(event.get("resumes", 0))
    state = runs.get_raw(svc, demo_id)
    if not state or state["runId"] != run_id or state["status"] not in (runs.LAUNCHING, runs.TEARING_DOWN):
        log.warning("Ignoring a runner invocation for a run that is no longer current: %s", demo_id)
        return "stale"

    destroy = state["action"] == runs.DESTROY
    failed = runs.TEARDOWN_FAILED if destroy else runs.LAUNCH_FAILED
    started = time.time()
    creds = None
    tail = terraform.LogTail([])
    try:
        if state.get("needsConfluent"):
            creds = credentials.load(svc, state["credsOwner"])
            if creds is None:
                raise RuntimeError("The saved Confluent Cloud credentials are gone. Save them again and retry")
            tail = terraform.LogTail([creds.api_secret, creds.api_key])
        tail.add(f"Launchpad: {'destroying' if destroy else 'applying'} {demo_id} at {state['ref'][:12]} (slice {resumes + 1})")

        WORK.mkdir(parents=True, exist_ok=True)
        (WORK / "home").mkdir(exist_ok=True)
        demo_dir = source.download(svc, state["ref"], state["path"], WORK / "src" / demo_id)
        problems = interface.check(source.read_tf(demo_dir)).problems
        if problems:
            raise RuntimeError("The demo does not follow the Launchpad interface: " + "; ".join(problems))
        tf_dir = demo_dir / "tf"
        (tf_dir / "zz_launchpad_backend.tf").write_text('terraform {\n  backend "s3" {}\n}\n')

        binary = terraform.ensure_binary(svc, WORK / "bin")
        tf = terraform.Terraform(
            binary,
            tf_dir,
            build_env(svc, demo_id, creds),
            tail,
            on_progress=lambda: runs.update_log(svc, demo_id, run_id, tail.text()),
        )

        init = tf.run("init", "-input=false", "-reconfigure", *backend_args(svc, demo_id))
        if init.code != 0:
            raise RuntimeError("terraform init failed")

        budget = remaining_seconds() - RESERVE_SECONDS
        if budget < MIN_USEFUL_SECONDS:
            return _resume(svc, demo_id, run_id, resumes, tail, failed, "not enough time left")
        deadline = time.time() + budget

        verb = "destroy" if destroy else "apply"
        args = [verb, "-auto-approve", "-input=false", "-lock-timeout=60s"]
        result = tf.run(*args, deadline=deadline)
        if result.code != 0 and not result.interrupted and "Error acquiring the state lock" in result.output:
            # A previous slice was cut off before it released the lock. The run id
            # proves no other run of this demo is active, so the lock is stale.
            m = _LOCK_ID.search(result.output)
            if m:
                tail.add("Launchpad: clearing a stale state lock left by an earlier slice")
                tf.run("force-unlock", "-force", m.group(1))
                result = tf.run(*args, deadline=deadline)

        if result.interrupted and result.code != 0:
            return _resume(svc, demo_id, run_id, resumes, tail, failed, "ran out of time")
        if result.code != 0:
            raise RuntimeError(f"terraform {verb} failed")

        url = ""
        if not destroy:
            out = tf.run("output", "-raw", "launchpad_url")
            url = out.output.splitlines()[-1].strip() if out.code == 0 and out.output.strip() else ""
        metrics.emit("TerraformSeconds", time.time() - started, "Seconds", action=verb)
        if destroy:
            runs.finish(svc, demo_id, run_id, None)
            return "destroyed"
        runs.finish(svc, demo_id, run_id, runs.RUNNING, url=url, tail=tail.text())
        metrics.emit("DemoLaunchSucceeded", demo=demo_id)
        return "running"
    except Exception as e:
        log.exception("Run failed: %s", demo_id)
        message = str(e) if isinstance(e, RuntimeError) else f"{type(e).__name__}: {e}"
        runs.finish(svc, demo_id, run_id, failed, error=message[:1000], tail=tail.text())
        metrics.emit("DemoTeardownFailed" if destroy else "DemoLaunchFailed", demo=demo_id)
        return "failed"


def _resume(svc: Services, demo_id: str, run_id: str, resumes: int, tail: terraform.LogTail, failed: str, why: str) -> str:
    if resumes >= MAX_RESUMES:
        runs.finish(svc, demo_id, run_id, failed, error=f"Not finished after {MAX_RESUMES + 1} attempts", tail=tail.text())
        metrics.emit("DemoLaunchFailed" if failed == runs.LAUNCH_FAILED else "DemoTeardownFailed", demo=demo_id)
        return "failed"
    tail.add(f"Launchpad: {why}, continuing in a new invocation")
    if not runs.update_log(svc, demo_id, run_id, tail.text(), resumes=resumes + 1):
        return "stale"
    runs.invoke_resume(svc, demo_id, run_id, resumes + 1)
    return "resumed"
