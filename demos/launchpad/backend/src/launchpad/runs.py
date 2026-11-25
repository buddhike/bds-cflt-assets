"""A demo's deployment: requests to launch or tear it down, and their outcome.

One item per demo holds its state. Every change is a conditional write, so two
clicks, or a stale runner, cannot start or finish the wrong run. See the state
machine in docs/specs/002_design.md.
"""

import json
import secrets

from . import catalog, clock, credentials, metrics, users
from .db import ConditionFailed
from .errors import ApiError, conflict, forbidden, not_found
from .services import Services

LAUNCHING, RUNNING, LAUNCH_FAILED = "launching", "running", "launch_failed"
TEARING_DOWN, TEARDOWN_FAILED = "tearing_down", "teardown_failed"
APPLY, DESTROY = "apply", "destroy"


def _key(demo_id: str) -> str:
    return f"DEMO#{demo_id}"


def get_raw(svc: Services, demo_id: str) -> dict | None:
    return svc.store.get(_key(demo_id), "STATE")


def public(state: dict | None) -> dict | None:
    if state is None:
        return None
    return {
        "status": state["status"],
        "url": state.get("url", ""),
        "error": state.get("error", ""),
        "logTail": state.get("logTail", ""),
        "launchedBy": state.get("launchedBy", ""),
        "updatedAt": state["updatedAt"],
        "ref": state.get("ref", ""),
    }


def _invoke(svc: Services, demo_id: str, run_id: str, resumes: int = 0) -> None:
    svc.lambda_client.invoke(
        FunctionName=svc.settings.runner_function,
        InvocationType="Event",
        Payload=json.dumps({"demoId": demo_id, "runId": run_id, "resumes": resumes}).encode(),
    )


def invoke_resume(svc: Services, demo_id: str, run_id: str, resumes: int) -> None:
    _invoke(svc, demo_id, run_id, resumes)


def request_launch(svc: Services, demo_id: str, email: str) -> dict:
    demo = catalog.get(svc, demo_id)
    if demo is None:
        raise not_found("No such demo")
    if not demo["valid"]:
        raise conflict("invalid_demo", "This demo does not follow the Launchpad interface: " + "; ".join(demo["problems"]))
    if demo["needsConfluent"] and not credentials.status(svc, email)["configured"]:
        raise conflict("credentials_required", "Save your Confluent Cloud API key before launching this demo")

    run_id = secrets.token_hex(8)
    now = clock.now()
    try:
        svc.store.put(
            {
                "pk": _key(demo_id),
                "sk": "STATE",
                "status": LAUNCHING,
                "action": APPLY,
                "runId": run_id,
                "ref": demo["ref"],
                "path": demo["path"],
                "needsConfluent": demo["needsConfluent"],
                "launchedBy": email,
                "credsOwner": email,
                "resumes": 0,
                "updatedAt": now,
            },
            condition="attribute_not_exists(pk) OR #s = :failed",
            names={"#s": "status"},
            values={":failed": LAUNCH_FAILED},
        )
    except ConditionFailed:
        current = get_raw(svc, demo_id) or {}
        raise conflict("not_launchable", f"The demo is {current.get('status', 'busy')}") from None
    metrics.emit("DemoLaunchRequested", demo=demo_id)
    _start(svc, demo_id, run_id, LAUNCH_FAILED)
    return public(get_raw(svc, demo_id))  # type: ignore[return-value]


def request_teardown(svc: Services, demo_id: str, user: dict) -> dict:
    state = get_raw(svc, demo_id)
    if state is None:
        raise not_found("The demo is not deployed")
    if user["role"] != users.ADMIN and user["email"] != state["launchedBy"]:
        raise forbidden("Only the person who launched a demo, or an admin, can tear it down")

    owner = None
    if state.get("needsConfluent"):
        # Destroying needs the same Confluent Cloud account that created things.
        for candidate in (state["launchedBy"], user["email"]):
            if credentials.status(svc, candidate)["configured"]:
                owner = candidate
                break
        if owner is None:
            raise conflict("credentials_required", "Save your Confluent Cloud API key before tearing this demo down")

    run_id = secrets.token_hex(8)
    previous = state["status"]
    try:
        svc.store.update(
            _key(demo_id),
            "STATE",
            "SET #s = :s, #a = :a, runId = :r, credsOwner = :o, resumes = :z, updatedAt = :t, #e = :empty",
            {
                ":s": TEARING_DOWN,
                ":a": DESTROY,
                ":r": run_id,
                ":o": owner or state["credsOwner"],
                ":z": 0,
                ":t": clock.now(),
                ":empty": "",
                ":prev": previous,
                ":old": state["runId"],
                ":ok1": RUNNING,
                ":ok2": LAUNCH_FAILED,
                ":ok3": TEARDOWN_FAILED,
            },
            names={"#s": "status", "#a": "action", "#e": "error"},
            condition="runId = :old AND #s = :prev AND #s IN (:ok1, :ok2, :ok3)",
        )
    except ConditionFailed:
        raise conflict("not_tearable", f"The demo is {previous} and cannot be torn down now") from None
    metrics.emit("DemoTeardownRequested", demo=demo_id)
    _start(svc, demo_id, run_id, TEARDOWN_FAILED)
    return public(get_raw(svc, demo_id))  # type: ignore[return-value]


def _start(svc: Services, demo_id: str, run_id: str, failed_status: str) -> None:
    try:
        _invoke(svc, demo_id, run_id)
    except Exception as e:  # the runner could not even be started
        finish(svc, demo_id, run_id, failed_status, error=f"Could not start the runner: {type(e).__name__}")
        raise ApiError(502, "runner_unavailable", "The deployment worker could not be started. Try again") from None


def update_log(svc: Services, demo_id: str, run_id: str, tail: str, resumes: int | None = None) -> bool:
    """Record the latest output. False if this run is no longer the current one."""
    expr, values = "SET logTail = :l, updatedAt = :t", {":l": tail, ":t": clock.now(), ":r": run_id}
    if resumes is not None:
        expr += ", resumes = :n"
        values[":n"] = resumes
    try:
        svc.store.update(_key(demo_id), "STATE", expr, values, condition="runId = :r")
        return True
    except ConditionFailed:
        return False


def finish(svc: Services, demo_id: str, run_id: str, status: str | None, url: str = "", error: str = "", tail: str | None = None) -> bool:
    """End a run. status None means the demo is gone (teardown succeeded)."""
    try:
        if status is None:
            svc.store.delete(_key(demo_id), "STATE", condition="runId = :r", values={":r": run_id})
            return True
        expr = "SET #s = :s, #u = :u, #e = :e, updatedAt = :t"
        values = {":s": status, ":u": url, ":e": error, ":t": clock.now(), ":r": run_id}
        if tail is not None:
            expr += ", logTail = :l"
            values[":l"] = tail
        svc.store.update(
            _key(demo_id), "STATE", expr, values, names={"#s": "status", "#u": "url", "#e": "error"}, condition="runId = :r"
        )
        return True
    except ConditionFailed:
        return False


def states_for(svc: Services, demo_ids: list[str]) -> dict[str, dict]:
    """The public state of each of these demos that is deployed, by demo id."""
    out = {}
    for demo_id in demo_ids:
        state = public(get_raw(svc, demo_id))
        if state:
            out[demo_id] = state
    return out
