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
    monkeypatch.setattr("launchpad.sms.send_code", lambda s, number, otp, m: mails.append(otp))
    monkeypatch.setattr(svc.lambda_client, "invoke", lambda **kw: {})

    class App:
        def call(self, method, path, body=None, token=None, headers=None):
            h = {"x-launchpad": "1", **(headers or {})}
            cookies = {api.COOKIE: token} if token else {}
            raw = json.dumps(body).encode() if body is not None else b""
            return api.dispatch(svc, api.Request(method, path, h, cookies, raw))

        def sign_in(self):
            self.call("POST", "/auth/start", {"resend": True})
            return self.call("POST", "/auth/verify", {"otp": mails[-1]})

        def token(self):
            r = self.sign_in()
            return r.cookies[0].split(";")[0].split("=", 1)[1]

    a = App()
    a.svc, a.mails = svc, mails
    return a


def test_sign_in_sets_a_hardened_cookie(app):
    r = app.sign_in()
    assert r.status == 200 and r.body["user"]["role"] == "admin" and "token" not in r.body
    cookie = r.cookies[0]
    assert all(flag in cookie for flag in ("HttpOnly", "Secure", "SameSite=Strict", "Path=/api"))
    assert app.call("GET", "/me", token=cookie.split(";")[0].split("=", 1)[1]).status == 200


def test_arriving_without_a_session_sends_one_code_and_reloading_sends_no_more(app, now):
    assert app.call("GET", "/me").status == 401
    r = app.call("POST", "/auth/start")
    assert r.status == 202 and r.body == {"sent": True} and len(app.mails) == 1
    now.t += auth.RESEND_SECONDS + 1
    r = app.call("POST", "/auth/start")
    assert r.status == 202 and r.body == {"sent": False} and len(app.mails) == 1
    assert app.call("POST", "/auth/verify", {"otp": app.mails[-1]}).status == 200


def test_the_request_cannot_choose_where_the_code_goes(app):
    app.call("POST", "/auth/start", {"email": "ann@confluent.io", "phone": "+61499999999", "PhoneNumber": "+61499999999"})
    assert len(app.mails) == 1 and app.svc.store.get("OTP#owner", "OTP")


def test_the_admin_routes_are_gone(app):
    token = app.token()
    for method, path in [("GET", "/admin/requests"), ("GET", "/admin/users"), ("POST", "/admin/users/ann@confluent.io/approve")]:
        assert app.call(method, path, token=token).status == 404


def test_mutations_need_the_csrf_header(app):
    token = app.token()
    r = app.call("PUT", "/credentials", {"apiKey": "KEY12345", "apiSecret": "secretsecret"}, token=token, headers={"x-launchpad": "0"})
    assert r.status == 403
    assert app.call("GET", "/credentials", token=token).body == {"configured": False}


def test_credentials_flow_never_returns_the_secret(app):
    token = app.token()
    r = app.call("PUT", "/credentials", {"apiKey": "KEY12345", "apiSecret": "topsecretvalue"}, token=token)
    assert r.body["configured"] and r.body["apiKeyHint"] == "2345"
    assert "topsecretvalue" not in json.dumps(app.call("GET", "/me", token=token).body)
    assert app.call("DELETE", "/credentials", token=token).body == {"configured": False}


def test_demo_list_launch_and_view(app):
    token = app.token()
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


def test_logout_clears_the_cookie_and_the_session(app):
    token = app.token()
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


def test_unexpected_errors_do_not_leak(app, monkeypatch):
    token = app.token()
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
