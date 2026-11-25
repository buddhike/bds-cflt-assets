"""Lambda entry point for the HTTP API (API Gateway payload format 2.0)."""

import base64
import json

from .. import api
from ..services import Services

_svc: Services | None = None


def _services() -> Services:
    global _svc
    if _svc is None:
        _svc = Services.from_env()
    return _svc


def to_request(event: dict) -> api.Request:
    path = event.get("rawPath", "/")
    path = path[len("/api") :] if path.startswith("/api/") else path
    cookies = {}
    for c in event.get("cookies") or []:
        name, _, value = c.partition("=")
        cookies[name.strip()] = value.strip()
    body = event.get("body") or ""
    raw = base64.b64decode(body) if event.get("isBase64Encoded") else body.encode()
    return api.Request(
        method=event["requestContext"]["http"]["method"].upper(),
        path=path.rstrip("/") or "/",
        headers={k.lower(): v for k, v in (event.get("headers") or {}).items()},
        cookies=cookies,
        body=raw,
    )


def handler(event, context):
    resp = api.dispatch(_services(), to_request(event))
    out = {
        "statusCode": resp.status,
        "headers": {"content-type": "application/json", "cache-control": "no-store"},
        "body": json.dumps(resp.body if resp.body is not None else {}),
    }
    if resp.cookies:
        out["cookies"] = resp.cookies
    return out
