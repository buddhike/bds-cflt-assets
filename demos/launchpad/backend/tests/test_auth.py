import pytest

from launchpad import auth, users
from launchpad.errors import ApiError


@pytest.fixture
def sent(svc, monkeypatch):
    """Capture the OTP the mailer would send."""
    box = []
    monkeypatch.setattr("launchpad.mailer.send_otp", lambda s, email, otp, minutes: box.append((email, otp)))
    return box


def sign_in(svc, sent, email):
    auth.start(svc, email)
    return auth.verify(svc, email, sent[-1][1])


def test_super_user_is_approved_and_gets_a_session(svc, sent):
    res = sign_in(svc, sent, "Boss@Confluent.io ")
    assert res["status"] == "approved" and res["user"]["role"] == "admin"
    assert auth.session_user(svc, res["token"])["email"] == "boss@confluent.io"


def test_other_user_waits_in_review_without_a_session(svc, sent):
    res = sign_in(svc, sent, "ann@confluent.io")
    assert res["status"] == "pending" and "token" not in res
    assert [u["email"] for u in users.list_requests(svc)] == ["ann@confluent.io"]


def test_domain_is_enforced(svc):
    for bad in ["ann@gmail.com", "ann@sub.confluent.io", "nope", "", None, 5]:
        with pytest.raises(ApiError) as e:
            auth.start(svc, bad)
        assert e.value.status == 400


def test_wrong_code_is_rejected_and_counted(svc, sent):
    auth.start(svc, "ann@confluent.io")
    wrong = "000000" if sent[-1][1] != "000000" else "111111"
    for _ in range(auth.MAX_GUESSES):
        with pytest.raises(ApiError):
            auth.verify(svc, "ann@confluent.io", wrong)
    # After the limit even the right code fails.
    with pytest.raises(ApiError):
        auth.verify(svc, "ann@confluent.io", sent[-1][1])


def test_code_is_single_use_and_expires(svc, sent, now):
    auth.start(svc, "boss@confluent.io")
    code = sent[-1][1]
    auth.verify(svc, "boss@confluent.io", code)
    with pytest.raises(ApiError):
        auth.verify(svc, "boss@confluent.io", code)
    now.t += auth.RESEND_SECONDS
    auth.start(svc, "boss@confluent.io")
    code = sent[-1][1]
    now.t += auth.OTP_MINUTES * 60
    with pytest.raises(ApiError):
        auth.verify(svc, "boss@confluent.io", code)


def test_resend_and_hourly_limits(svc, sent, now):
    auth.start(svc, "ann@confluent.io")
    with pytest.raises(ApiError) as e:
        auth.start(svc, "ann@confluent.io")
    assert e.value.status == 429
    for _ in range(auth.MAX_SENDS_PER_HOUR - 1):
        now.t += auth.RESEND_SECONDS
        auth.start(svc, "ann@confluent.io")
    now.t += auth.RESEND_SECONDS
    with pytest.raises(ApiError) as e:
        auth.start(svc, "ann@confluent.io")
    assert e.value.code == "too_many"
    now.t += 3600
    auth.start(svc, "ann@confluent.io")


def test_code_format_is_checked_before_anything_is_read(svc):
    for bad in ["12345", "abcdef", "1234567", None, 123456]:
        with pytest.raises(ApiError):
            auth.verify(svc, "ann@confluent.io", bad)


def test_otp_is_not_stored_in_clear(svc, sent):
    auth.start(svc, "ann@confluent.io")
    item = svc.store.get("OTP#ann@confluent.io", "OTP")
    assert sent[-1][1] not in item.values()


def test_session_expiry_and_logout(svc, sent, now):
    token = sign_in(svc, sent, "boss@confluent.io")["token"]
    auth.session_user(svc, token)
    auth.logout(svc, token)
    with pytest.raises(ApiError) as e:
        auth.session_user(svc, token)
    assert e.value.status == 401
    now.t += auth.RESEND_SECONDS
    token = sign_in(svc, sent, "boss@confluent.io")["token"]
    now.t += svc.settings.session_hours * 3600
    with pytest.raises(ApiError):
        auth.session_user(svc, token)
    with pytest.raises(ApiError):
        auth.session_user(svc, None)


def test_approved_user_can_sign_in_and_denial_ends_the_session(svc, sent, now):
    assert sign_in(svc, sent, "ann@confluent.io")["status"] == "pending"
    users.decide(svc, "boss@confluent.io", "ann@confluent.io", approve=True)
    now.t += auth.RESEND_SECONDS
    token = sign_in(svc, sent, "ann@confluent.io")["token"]
    users.decide(svc, "boss@confluent.io", "ann@confluent.io", approve=False)
    with pytest.raises(ApiError) as e:
        auth.session_user(svc, token)
    assert e.value.status == 401
    now.t += auth.RESEND_SECONDS
    assert sign_in(svc, sent, "ann@confluent.io")["status"] == "denied"


def test_admin_cannot_be_denied(svc, sent):
    sign_in(svc, sent, "boss@confluent.io")
    with pytest.raises(ApiError) as e:
        users.decide(svc, "boss@confluent.io", "boss@confluent.io", approve=False)
    assert e.value.status == 403


def test_deciding_unknown_user_is_404(svc):
    with pytest.raises(ApiError) as e:
        users.decide(svc, "boss@confluent.io", "ghost@confluent.io", approve=True)
    assert e.value.status == 404


def test_list_all_orders_by_signup(svc, sent, now):
    sign_in(svc, sent, "boss@confluent.io")
    now.t += 100
    sign_in(svc, sent, "ann@confluent.io")
    users.decide(svc, "boss@confluent.io", "ann@confluent.io", approve=True)
    assert [(u["email"], u["status"]) for u in users.list_all(svc)] == [
        ("boss@confluent.io", "approved"),
        ("ann@confluent.io", "approved"),
    ]
    assert users.list_requests(svc) == []


def test_real_mailer_sends_the_code_by_ses(svc):
    from moto.ses import ses_backends

    auth.start(svc, "boss@confluent.io")
    (msg,) = ses_backends["123456789012"]["us-east-1"].sent_messages
    assert msg.destinations["ToAddresses"] == ["boss@confluent.io"]
    assert msg.source == "launchpad@example.com"
    code = svc.store.get("OTP#boss@confluent.io", "OTP")
    assert code["hash"] not in msg.body  # the hash is not what was mailed
    assert len([w for w in msg.body.split() if w.isdigit() and len(w) == 6]) == 1


def test_mailer_failure_is_a_502_without_detail(svc):
    svc.settings = svc.settings.__class__(**{**svc.settings.__dict__, "sender_email": "unverified@nowhere.test"})
    with pytest.raises(ApiError) as e:
        auth.start(svc, "boss@confluent.io")
    assert e.value.status == 502
    assert "unverified" not in e.value.message
