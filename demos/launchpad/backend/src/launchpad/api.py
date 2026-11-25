"""The HTTP API: routing, sessions and JSON, with no AWS event format in it."""

import json
import logging
import re
from dataclasses import dataclass, field
from typing import Callable

from . import auth, catalog, credentials, runs, users
from .errors import ApiError, bad_request, forbidden, not_found
from .services import Services

log = logging.getLogger(__name__)

COOKIE = "lp_session"
MAX_BODY = 10_000


@dataclass
class Request:
    method: str
    path: str  # without the /api prefix
    headers: dict[str, str]  # lower-case names
    cookies: dict[str, str]
    body: bytes = b""

    def json(self) -> dict:
        if len(self.body) > MAX_BODY:
            raise ApiError(413, "too_large", "The request is too large")
        if not self.body:
            return {}
        try:
            data = json.loads(self.body)
        except ValueError:
            raise bad_request("invalid_json", "The request is not valid JSON") from None
        if not isinstance(data, dict):
            raise bad_request("invalid_json", "The request must be a JSON object")
        return data

    @property
    def token(self) -> str | None:
        return self.cookies.get(COOKIE)


@dataclass
class Response:
    status: int = 200
    body: object = None
    cookies: list[str] = field(default_factory=list)


def session_cookie(token: str, max_age: int) -> str:
    return f"{COOKIE}={token}; Max-Age={max_age}; Path=/api; HttpOnly; Secure; SameSite=Strict"


def _user_view(svc: Services, user: dict) -> dict:
    return {**users.public(user), "credentials": credentials.status(svc, user["email"])}


def _require_user(svc: Services, req: Request) -> dict:
    return auth.session_user(svc, req.token)


def _require_admin(svc: Services, req: Request) -> dict:
    user = _require_user(svc, req)
    if user["role"] != users.ADMIN:
        raise forbidden("Only admins can do this")
    return user


# --- sign in -------------------------------------------------------------


def start_sign_in(svc, req, _):
    auth.start(svc, req.json().get("phone"))
    return Response(202, {"sent": True})  # always: see auth.start


def verify_sign_in(svc, req, _):
    result = auth.verify(svc, req.json().get("otp"))
    token = result.pop("token")
    return Response(200, result, [session_cookie(token, svc.settings.session_hours * 3600)])


def sign_out(svc, req, _):
    auth.logout(svc, req.token)
    return Response(200, {"ok": True}, [session_cookie("", 0)])


def me(svc, req, _):
    return Response(200, _user_view(svc, _require_user(svc, req)))


# --- credentials ---------------------------------------------------------


def get_credentials(svc, req, _):
    return Response(200, credentials.status(svc, _require_user(svc, req)["email"]))


def put_credentials(svc, req, _):
    user = _require_user(svc, req)
    body = req.json()
    return Response(200, credentials.save(svc, user["email"], body.get("apiKey"), body.get("apiSecret")))


def delete_credentials(svc, req, _):
    credentials.delete(svc, _require_user(svc, req)["email"])
    return Response(200, {"configured": False})


# --- demos ---------------------------------------------------------------


def _demo_view(user: dict, demo: dict, state: dict | None, with_log: bool) -> dict:
    view = {k: demo[k] for k in ("id", "name", "description", "valid", "problems", "needsConfluent")}
    if state:
        state = dict(state)
        if not with_log:
            state.pop("logTail", None)
        state["canTearDown"] = user["role"] == users.ADMIN or user["email"] == state["launchedBy"]
    view["state"] = state
    return view


def list_demos(svc, req, _):
    user = _require_user(svc, req)
    demos = catalog.list_demos(svc)
    states = runs.states_for(svc, [d["id"] for d in demos])
    return Response(200, {"demos": [_demo_view(user, d, states.get(d["id"]), False) for d in demos]})


def refresh_demos(svc, req, _):
    user = _require_admin(svc, req)
    demos = catalog.list_demos(svc, force=True)
    states = runs.states_for(svc, [d["id"] for d in demos])
    return Response(200, {"demos": [_demo_view(user, d, states.get(d["id"]), False) for d in demos]})


def get_demo(svc, req, params):
    user = _require_user(svc, req)
    demo = catalog.get(svc, params["id"])
    if demo is None:
        raise not_found("No such demo")
    return Response(200, _demo_view(user, demo, runs.public(runs.get_raw(svc, demo["id"])), True))


def launch(svc, req, params):
    user = _require_user(svc, req)
    runs.request_launch(svc, params["id"], user["email"])
    return get_demo(svc, req, params)


def teardown(svc, req, params):
    user = _require_user(svc, req)
    runs.request_teardown(svc, params["id"], user)
    return get_demo(svc, req, params)


Route = Callable[[Services, Request, dict], Response]
ROUTES: list[tuple[str, re.Pattern, Route]] = []


def _route(method: str, pattern: str, fn: Route) -> None:
    regex = re.sub(r"\{(\w+)\}", r"(?P<\1>[^/]+)", pattern)
    ROUTES.append((method, re.compile(f"^{regex}$"), fn))


_route("POST", "/auth/start", start_sign_in)
_route("POST", "/auth/verify", verify_sign_in)
_route("POST", "/auth/logout", sign_out)
_route("GET", "/me", me)
_route("GET", "/credentials", get_credentials)
_route("PUT", "/credentials", put_credentials)
_route("DELETE", "/credentials", delete_credentials)
_route("GET", "/demos", list_demos)
_route("POST", "/demos/refresh", refresh_demos)
_route("GET", "/demos/{id}", get_demo)
_route("POST", "/demos/{id}/launch", launch)
_route("POST", "/demos/{id}/teardown", teardown)


def dispatch(svc: Services, req: Request) -> Response:
    """Run a request. Failures become JSON error responses."""
    try:
        path_known = False
        for method, regex, fn in ROUTES:
            m = regex.match(req.path)
            if not m:
                continue
            path_known = True
            if method != req.method:
                continue
            # SameSite=Strict already keeps other sites from sending the cookie. Requiring a header
            # a form cannot set is a second, independent guard.
            if req.method != "GET" and req.headers.get("x-launchpad") != "1":
                raise forbidden("Missing request header")
            return fn(svc, req, m.groupdict())
        if path_known:
            raise ApiError(405, "method_not_allowed", "Method not allowed")
        raise not_found()
    except ApiError as e:
        return Response(e.status, {"error": {"code": e.code, "message": e.message}})
    except Exception:
        log.exception("Unhandled error")
        return Response(500, {"error": {"code": "internal", "message": "Something went wrong"}})
