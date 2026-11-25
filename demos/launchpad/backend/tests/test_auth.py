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
    auth.start(svc, OWNER_PHONE)
    return auth.verify(svc, sent[-1][1])


def test_code_goes_to_the_owner_and_signs_them_in(svc, sent):
    res = sign_in(svc, sent)
    assert sent[-1][0] == OWNER_PHONE
    assert res["user"]["email"] == "boss@confluent.io" and res["user"]["role"] == "admin"
    assert auth.session_user(svc, res["token"])["email"] == "boss@confluent.io"


# --- the number the page asks for -----------------------------------------


def test_another_number_sends_nothing_and_looks_the_same(svc, sent):
    assert auth.start(svc, "+61400000099") is None
    assert auth.start(svc, OWNER_PHONE) is None  # the same answer for the right one
    assert [n for n, _ in sent] == [OWNER_PHONE]


def test_another_number_leaves_no_trace_that_could_replace_a_code(svc, sent, now):
    auth.start(svc, OWNER_PHONE)
    code = sent[-1][1]
    now.t += auth.RESEND_SECONDS + 1
    for _ in range(20):
        auth.start(svc, "+61400000099")
    assert len(sent) == 1
    assert auth.verify(svc, code)["token"]


@pytest.mark.parametrize("typed", ["+61 400 000 001", "+61-400-000-001", " +61(400)000.001 ", "+61400000001"])
def test_the_number_may_be_typed_with_spaces_and_dashes(svc, sent, typed):
    auth.start(svc, typed)
    assert [n for n, _ in sent] == [OWNER_PHONE]


@pytest.mark.parametrize("typed", ["", "0400000001", "61400000001", "+0400000001", "abc", "+61400", "+" + "9" * 20, None, 61400000001, ["+61400000001"]])
def test_something_that_is_not_a_phone_number_is_a_400(svc, sent, typed):
    with pytest.raises(ApiError) as e:
        auth.start(svc, typed)
    assert e.value.status == 400 and e.value.code == "invalid_number"
    assert sent == []


def test_the_400_does_not_depend_on_the_stored_number(svc, sent):
    first = _error(lambda: auth.start(svc, "0400"))
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString="+61400000002")
    assert _error(lambda: auth.start(svc, "0400")) == first


def _error(fn):
    with pytest.raises(ApiError) as e:
        fn()
    return e.value.status, e.value.code, e.value.message


def test_a_secret_that_is_not_set_up_still_looks_like_a_send(svc, sent):
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString="REPLACE_WITH_A_PHONE_NUMBER")
    assert auth.start(svc, OWNER_PHONE) is None
    assert sent == [] and svc.store.get("OTP#owner", "OTP") is None


def test_a_failed_send_still_looks_like_a_send(svc):
    svc.sns = RecordingSns(fail=True)
    assert auth.start(svc, OWNER_PHONE) is None


def test_a_crash_still_looks_like_a_send(svc, monkeypatch):
    monkeypatch.setattr(svc.store, "put", lambda *a, **k: 1 / 0)
    assert auth.start(svc, OWNER_PHONE) is None


def test_every_reason_for_not_sending_is_counted(svc, sent, now, capsys):
    auth.start(svc, "+61400000099")
    auth.start(svc, OWNER_PHONE)
    auth.start(svc, OWNER_PHONE)  # held back by the 30 second limit
    out = capsys.readouterr().out
    assert '"reason":"number_mismatch"' in out and '"reason":"too_soon"' in out
    assert "+61400000099" not in out and OWNER_PHONE not in out


# --- the code ---------------------------------------------------------------


def test_wrong_code_is_rejected_and_counted(svc, sent):
    auth.start(svc, OWNER_PHONE)
    wrong = "000000" if sent[-1][1] != "000000" else "111111"
    for _ in range(auth.MAX_GUESSES):
        with pytest.raises(ApiError):
            auth.verify(svc, wrong)
    # After the limit even the right code fails.
    with pytest.raises(ApiError):
        auth.verify(svc, sent[-1][1])


def test_no_code_can_be_guessed_before_one_is_sent(svc):
    with pytest.raises(ApiError):
        auth.verify(svc, "123456")


def test_code_is_single_use_and_expires(svc, sent, now):
    auth.start(svc, OWNER_PHONE)
    code = sent[-1][1]
    auth.verify(svc, code)
    with pytest.raises(ApiError):
        auth.verify(svc, code)
    now.t += auth.RESEND_SECONDS
    auth.start(svc, OWNER_PHONE)
    code = sent[-1][1]
    now.t += auth.OTP_MINUTES * 60
    with pytest.raises(ApiError):
        auth.verify(svc, code)


def test_a_new_code_replaces_the_old_one(svc, sent, now):
    auth.start(svc, OWNER_PHONE)
    old = sent[-1][1]
    now.t += auth.RESEND_SECONDS
    auth.start(svc, OWNER_PHONE)
    assert len(sent) == 2
    if old != sent[-1][1]:
        with pytest.raises(ApiError):
            auth.verify(svc, old)
    assert auth.verify(svc, sent[-1][1])["token"]


def test_resend_and_hourly_limits(svc, sent, now):
    auth.start(svc, OWNER_PHONE)
    auth.start(svc, OWNER_PHONE)  # too soon: no second message, and no error either
    assert len(sent) == 1
    for _ in range(auth.MAX_SENDS_PER_HOUR - 1):
        now.t += auth.RESEND_SECONDS
        auth.start(svc, OWNER_PHONE)
    assert len(sent) == auth.MAX_SENDS_PER_HOUR
    now.t += auth.RESEND_SECONDS
    auth.start(svc, OWNER_PHONE)
    assert len(sent) == auth.MAX_SENDS_PER_HOUR
    now.t += 3600
    auth.start(svc, OWNER_PHONE)
    assert len(sent) == auth.MAX_SENDS_PER_HOUR + 1


def test_code_format_is_checked_before_anything_is_read(svc):
    for bad in ["12345", "abcdef", "1234567", None, 123456]:
        with pytest.raises(ApiError):
            auth.verify(svc, bad)


def test_otp_is_not_stored_in_clear(svc, sent):
    auth.start(svc, OWNER_PHONE)
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
    auth.start(svc, OWNER_PHONE)
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
    auth.start(svc, OWNER_PHONE)
    (call,) = svc.sns.calls
    assert call["PhoneNumber"] == OWNER_PHONE and set(call) == {"PhoneNumber", "Message", "MessageAttributes"}
    assert call["MessageAttributes"]["AWS.SNS.SMS.SMSType"]["StringValue"] == "Transactional"
    code = [w.strip(".") for w in call["Message"].split() if w.strip(".").isdigit() and len(w.strip(".")) == 6]
    assert len(code) == 1


def test_the_secret_is_read_on_every_send(svc, now):
    svc.sns = RecordingSns()
    auth.start(svc, OWNER_PHONE)
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString="+61400000002")
    now.t += auth.RESEND_SECONDS
    auth.start(svc, "+61400000002")
    assert [c["PhoneNumber"] for c in svc.sns.calls] == [OWNER_PHONE, "+61400000002"]


def test_a_one_entry_json_secret_is_accepted(svc):
    svc.sns = RecordingSns()
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString='{"phone": " +61400000003 "}')
    auth.start(svc, "+61400000003")
    assert svc.sns.calls[0]["PhoneNumber"] == "+61400000003"


@pytest.mark.parametrize("value", ["REPLACE_WITH_A_PHONE_NUMBER", " ", "0400000000", "+61 400 000 001", "+0123456", '{"a": "+61400000001", "b": "+61400000002"}', "{not json", "[1]"])
def test_a_secret_that_is_not_a_number_is_a_503(svc, value):
    svc.secrets.put_secret_value(SecretId=svc.settings.sign_in_secret, SecretString=value)
    with pytest.raises(ApiError) as e:
        sms.sign_in_number(svc)
    assert e.value.status == 503 and e.value.code == "sign_in_unavailable"


def test_an_unreadable_secret_is_a_503(svc):
    svc.sns = RecordingSns()
    svc.settings = svc.settings.__class__(**{**svc.settings.__dict__, "sign_in_secret": "no-such-secret"})
    with pytest.raises(ApiError) as e:
        sms.sign_in_number(svc)
    assert e.value.status == 503


def test_a_failed_send_is_a_502_without_detail(svc):
    svc.sns = RecordingSns(fail=True)
    with pytest.raises(ApiError) as e:
        sms.send_code(svc, OWNER_PHONE, "123456", 10)
    assert e.value.status == 502
    assert "+614" not in e.value.message and "123" not in e.value.message


def test_publish_accepts_the_call_we_make(svc):
    # moto's SNS is the real client with a fake endpoint, so a malformed call fails here.
    sms.send_code(svc, OWNER_PHONE, "123456", 10)
