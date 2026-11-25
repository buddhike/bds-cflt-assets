import json

import pytest

from launchpad import api, auth, catalog, credentials, runs
from launchpad.handlers import api as lambda_api

from fakes import CC_TF, GOOD_TF, FakeGitHub


@pytest.fixture
def app(svc, monkeypatch):
    gh = FakeGitHub()
    gh.add_repo({"demos/pims/tf/main.tf": CC_TF, "demos/plain/tf/main.tf": GOOD_TF})
    svc.http = gh
    mails = []
    monkeypatch.setattr("launchpad.mailer.send_otp", lambda s, email, otp, m: mails.append(otp))
    monkeypatch.setattr(svc.lambda_client, "invoke", lambda **kw: {})

    class App:
        def call(self, method, path, body=None, token=None, headers=None):
            h = {"x-launchpad": "1", **(headers or {})}
            cookies = {api.COOKIE: token} if token else {}
            raw = json.dumps(body).encode() if body is not None else b""
            return api.dispatch(svc, api.Request(method, path, h, cookies, raw))

        def sign_in(self, email):
            self.call("POST", "/auth/start", {"email": email})
            r = self.call("POST", "/auth/verify", {"email": email, "otp": mails[-1]})
            return r

        def token(self, email):
            r = self.sign_in(email)
            return r.cookies[0].split(";")[0].split("=", 1)[1]

    a = App()
    a.svc, a.mails = svc, mails
    return a


def test_sign_in_sets_a_hardened_cookie(app):
    r = app.sign_in("boss@confluent.io")
    assert r.status == 200 and r.body["status"] == "approved" and "token" not in r.body
    cookie = r.cookies[0]
    assert all(flag in cookie for flag in ("HttpOnly", "Secure", "SameSite=Strict", "Path=/api"))
    assert app.call("GET", "/me", token=cookie.split(";")[0].split("=", 1)[1]).status == 200


def test_pending_user_gets_no_cookie_and_cannot_use_the_api(app):
    r = app.sign_in("ann@confluent.io")
    assert r.body["status"] == "pending" and r.cookies == []
    assert app.call("GET", "/demos").status == 401


def test_admin_reviews_requests_and_approves(app, now):
    admin = app.token("boss@confluent.io")
    app.sign_in("ann@confluent.io")
    assert [u["email"] for u in app.call("GET", "/admin/requests", token=admin).body["requests"]] == ["ann@confluent.io"]
    r = app.call("POST", "/admin/users/ann@confluent.io/approve", token=admin)
    assert r.status == 200 and r.body["status"] == "approved"
    now.t += auth.RESEND_SECONDS
    assert app.token("ann@confluent.io")


def test_non_admin_cannot_use_admin_routes(app, now):
    admin = app.token("boss@confluent.io")
    app.sign_in("ann@confluent.io")
    app.call("POST", "/admin/users/ann@confluent.io/approve", token=admin)
    now.t += auth.RESEND_SECONDS
    ann = app.token("ann@confluent.io")
    for method, path in [("GET", "/admin/requests"), ("GET", "/admin/users"), ("POST", "/admin/users/ann@confluent.io/deny"), ("POST", "/demos/refresh")]:
        assert app.call(method, path, token=ann).status == 403


def test_mutations_need_the_csrf_header(app):
    token = app.token("boss@confluent.io")
    r = app.call("PUT", "/credentials", {"apiKey": "KEY12345", "apiSecret": "secretsecret"}, token=token, headers={"x-launchpad": "0"})
    assert r.status == 403
    assert app.call("GET", "/credentials", token=token).body == {"configured": False}


def test_credentials_flow_never_returns_the_secret(app):
    token = app.token("boss@confluent.io")
    r = app.call("PUT", "/credentials", {"apiKey": "KEY12345", "apiSecret": "topsecretvalue"}, token=token)
    assert r.body["configured"] and r.body["apiKeyHint"] == "2345"
    assert "topsecretvalue" not in json.dumps(app.call("GET", "/me", token=token).body)
    assert app.call("DELETE", "/credentials", token=token).body == {"configured": False}


def test_demo_list_launch_and_view(app):
    token = app.token("boss@confluent.io")
    demos = {d["id"]: d for d in app.call("GET", "/demos", token=token).body["demos"]}
    assert set(demos) == {"pims", "plain"} and demos["pims"]["needsConfluent"] and demos["plain"]["state"] is None
    r = app.call("POST", "/demos/pims/launch", token=token)
    assert r.status == 409 and r.body["error"]["code"] == "credentials_required"
    r = app.call("POST", "/demos/plain/launch", token=token)
    assert r.status == 200 and r.body["state"]["status"] == "launching" and r.body["state"]["canTearDown"]
    listed = {d["id"]: d for d in app.call("GET", "/demos", token=token).body["demos"]}
    assert "logTail" not in listed["plain"]["state"]
    assert "logTail" in app.call("GET", "/demos/plain", token=token).body["state"]
    assert app.call("GET", "/demos/nope", token=token).status == 404


def test_teardown_permissions_are_reported_and_enforced(app, now):
    admin = app.token("boss@confluent.io")
    app.sign_in("ann@confluent.io")
    app.call("POST", "/admin/users/ann@confluent.io/approve", token=admin)
    now.t += auth.RESEND_SECONDS
    ann = app.token("ann@confluent.io")
    r = app.call("POST", "/demos/plain/launch", token=ann)
    assert r.status == 200, r.body
    run = runs.get_raw(app.svc, "plain")["runId"]
    runs.finish(app.svc, "plain", run, runs.RUNNING, url="http://x")
    assert app.call("GET", "/demos/plain", token=ann).body["state"]["canTearDown"]
    assert app.call("POST", "/demos/plain/teardown", token=ann).body["state"]["status"] == "tearing_down"


def test_logout_clears_the_cookie_and_the_session(app):
    token = app.token("boss@confluent.io")
    r = app.call("POST", "/auth/logout", token=token)
    assert "Max-Age=0" in r.cookies[0]
    assert app.call("GET", "/me", token=token).status == 401


def test_routing_errors_and_bad_bodies(app):
    assert app.call("GET", "/nope").status == 404
    assert app.call("DELETE", "/me").status == 405
    r = api.dispatch(app.svc, api.Request("POST", "/auth/start", {"x-launchpad": "1"}, {}, b"{not json"))
    assert r.status == 400 and r.body["error"]["code"] == "invalid_json"
    r = api.dispatch(app.svc, api.Request("POST", "/auth/start", {"x-launchpad": "1"}, {}, b"[]"))
    assert r.status == 400
    r = api.dispatch(app.svc, api.Request("POST", "/auth/start", {"x-launchpad": "1"}, {}, b"x" * 20000))
    assert r.status == 413
    assert app.call("POST", "/auth/start", {"email": "x@gmail.com"}).status == 400


def test_unexpected_errors_do_not_leak(app, monkeypatch):
    token = app.token("boss@confluent.io")
    monkeypatch.setattr(catalog, "list_demos", lambda *a, **k: 1 / 0)
    r = app.call("GET", "/demos", token=token)
    assert r.status == 500 and r.body["error"]["message"] == "Something went wrong"


def test_lambda_event_conversion():
    event = {
        "rawPath": "/api/credentials/",
        "cookies": ["lp_session=abc", "other=1"],
        "headers": {"X-Launchpad": "1"},
        "requestContext": {"http": {"method": "put"}},
        "body": "eyJhIjoxfQ==",
        "isBase64Encoded": True,
    }
    req = lambda_api.to_request(event)
    assert (req.method, req.path, req.token, req.headers["x-launchpad"], req.body) == ("PUT", "/credentials", "abc", "1", b'{"a":1}')
