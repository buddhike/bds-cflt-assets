import pytest
from conftest import OWNER_PHONE

from launchpad import auth, sms, users
from launchpad.errors import ApiError


@pytest.fixture
def sent(svc, monkeypatch):
    """Capture the text messages the sign-in would send, as (number, code)."""
    box = []
    monkeypatch.setattr("launchpad.sms.send_code", lambda s, number, otp, minutes: box.append((number, otp)))
    return box


def sign_in(svc, sent):
    auth.start(svc, resend=True)
    return auth.verify(svc, sent[-1][1])


def test_code_goes_to_the_owner_and_signs_them_in(svc, sent):
    res = sign_in(svc, sent)
    assert sent[-1][0] == OWNER_PHONE
    assert res["user"]["email"] == "boss@confluent.io" and res["user"]["role"] == "admin"
    assert auth.session_user(svc, res["token"])["email"] == "boss@confluent.io"


def test_wrong_code_is_rejected_and_counted(svc, sent):
    auth.start(svc)
    wrong = "000000" if sent[-1][1] != "000000" else "111111"
    for _ in range(auth.MAX_GUESSES):
        with pytest.raises(ApiError):
            auth.verify(svc, wrong)
    # After the limit even the right code fails.
    with pytest.raises(ApiError):
        auth.verify(svc, sent[-1][1])


def test_code_is_single_use_and_expires(svc, sent, now):
    auth.start(svc)
    code = sent[-1][1]
    auth.verify(svc, code)
    with pytest.raises(ApiError):
        auth.verify(svc, code)
    now.t += auth.RESEND_SECONDS
    auth.start(svc)
    code = sent[-1][1]
    now.t += auth.OTP_MINUTES * 60
    with pytest.raises(ApiError):
        auth.verify(svc, code)


def test_a_code_that_is_still_good_is_not_replaced(svc, sent, now):
    assert auth.start(svc) is True
    code = sent[-1][1]
    now.t += auth.RESEND_SECONDS + 1
    assert auth.start(svc) is False  # a page reload, or anyone, cannot replace the code
    assert len(sent) == 1
    assert auth.verify(svc, code)["token"]


def test_a_spent_or_expired_code_is_replaced_on_the_next_visit(svc, sent, now):
    auth.start(svc)
    for _ in range(auth.MAX_GUESSES):
        with pytest.raises(ApiError):
            auth.verify(svc, "000000" if sent[-1][1] != "000000" else "111111")
    now.t += auth.RESEND_SECONDS
    assert auth.start(svc) is True
    now.t += auth.OTP_MINUTES * 60
    assert auth.start(svc) is True
    assert len(sent) == 3


def test_resend_replaces_the_code_and_the_old_one_stops_working(svc, sent, now):
    auth.start(svc)
    old = sent[-1][1]
    now.t += auth.RESEND_SECONDS
    assert auth.start(svc, resend=True) is True
    if old != sent[-1][1]:
        with pytest.raises(ApiError):
            auth.verify(svc, old)
    assert auth.verify(svc, sent[-1][1])["token"]


def test_resend_and_hourly_limits(svc, sent, now):
    auth.start(svc)
    with pytest.raises(ApiError) as e:
        auth.start(svc, resend=True)
    assert e.value.status == 429 and e.value.code == "too_soon"
    for _ in range(auth.MAX_SENDS_PER_HOUR - 1):
        now.t += auth.RESEND_SECONDS
        auth.start(svc, resend=True)
    now.t += auth.RESEND_SECONDS
    with pytest.raises(ApiError) as e:
        auth.start(svc, resend=True)
    assert e.value.code == "too_many"
    now.t += 3600
    auth.start(svc, resend=True)


def test_code_format_is_checked_before_anything_is_read(svc):
    for bad in ["12345", "abcdef", "1234567", None, 123456]:
        with pytest.raises(ApiError):
            auth.verify(svc, bad)


def test_otp_is_not_stored_in_clear(svc, sent):
    auth.start(svc)
    item = svc.store.get("OTP#owner", "OTP")
    assert sent[-1][1] not in item.values()


def test_session_expiry_and_logout(svc, sent, now):
    token = sign_in(svc, sent)["token"]
    auth.session_user(svc, token)
    auth.logout(svc, token)
    with pytest.raises(ApiError) as e:
        auth.session_user(svc, token)
    assert e.value.status == 401
    now.t += auth.RESEND_SECONDS
    token = sign_in(svc, sent)["token"]
    now.t += svc.settings.session_hours * 3600
    with pytest.raises(ApiError):
        auth.session_user(svc, token)
    with pytest.raises(ApiError):
        auth.session_user(svc, None)


def test_a_new_owner_address_ends_the_old_sessions(svc, sent):
    token = sign_in(svc, sent)["token"]
    svc.settings = svc.settings.__class__(**{**svc.settings.__dict__, "super_user_email": "other@confluent.io"})
    with pytest.raises(ApiError) as e:
        auth.session_user(svc, token)
    assert e.value.status == 401


def test_owner_profile_is_created_once(svc, sent, now):
    sign_in(svc, sent)
    created = users.get(svc, "boss@confluent.io")["createdAt"]
    now.t += 100
    auth.start(svc, resend=True)
    auth.verify(svc, sent[-1][1])
    assert users.get(svc, "boss@confluent.io")["createdAt"] == created


# --- the real text message ------------------------------------------------


class RecordingSns:
    def __init__(self, fail=False):
        self.calls, self.fail = [], fail

    def publish(self, **kw):
        if self.fail:
            from botocore.exceptions import ClientError

            raise ClientError({"Error": {"Code": "AuthorizationError", "Message": "account 123 may not publish to +61400000001"}}, "Publish")
        self.calls.append(kw)
        return {"MessageId": "m"}


def test_the_text_goes_to_the_number_in_the_secret_and_only_there(svc):
    svc.sns = RecordingSns()
    auth.start(svc)
    (call,) = svc.sns.calls
    assert call["PhoneNumber"] == OWNER_PHONE and set(call) == {"PhoneNumber", "Message", "MessageAttributes"}
    assert call["MessageAttributes"]["AWS.SNS.SMS.SMSType"]["StringValue"] == "Transactional"
    code = [w.strip(".") for w in call["Message"].split() if w.strip(".").isdigit() and len(w.strip(".")) == 6]
    assert len(code) == 1


def test_the_secret_is_read_on_every_send(svc, now):
    svc.sns = RecordingSns()
    auth.start(svc)
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString="+61400000002")
    now.t += auth.RESEND_SECONDS
    auth.start(svc, resend=True)
    assert [c["PhoneNumber"] for c in svc.sns.calls] == [OWNER_PHONE, "+61400000002"]


def test_a_one_entry_json_secret_is_accepted(svc):
    svc.sns = RecordingSns()
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString='{"phone": " +61400000003 "}')
    auth.start(svc)
    assert svc.sns.calls[0]["PhoneNumber"] == "+61400000003"


@pytest.mark.parametrize("value", ["REPLACE_WITH_A_PHONE_NUMBER", " ", "0400000000", "+61 400 000 001", "+0123456", '{"a": "+61400000001", "b": "+61400000002"}', "{not json", "[1]"])
def test_a_secret_that_is_not_a_number_sends_nothing_and_uses_no_send(svc, value, now):
    svc.sns = RecordingSns()
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString=value)
    with pytest.raises(ApiError) as e:
        auth.start(svc)
    assert e.value.status == 503 and e.value.code == "sign_in_unavailable"
    assert svc.sns.calls == [] and svc.store.get("OTP#owner", "OTP") is None


def test_an_unreadable_secret_is_a_503(svc):
    svc.sns = RecordingSns()
    svc.settings = svc.settings.__class__(**{**svc.settings.__dict__, "sign_in_secret": "no-such-secret"})
    with pytest.raises(ApiError) as e:
        sms.sign_in_number(svc)
    assert e.value.status == 503


def test_a_failed_send_is_a_502_without_detail(svc):
    svc.sns = RecordingSns(fail=True)
    with pytest.raises(ApiError) as e:
        auth.start(svc)
    assert e.value.status == 502
    assert "+614" not in e.value.message and "123" not in e.value.message


def test_publish_accepts_the_call_we_make(svc):
    # moto's SNS is the real client with a fake endpoint, so a malformed call fails here.
    sms.send_code(svc, OWNER_PHONE, "123456", 10)
