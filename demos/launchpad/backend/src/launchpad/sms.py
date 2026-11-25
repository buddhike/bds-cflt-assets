"""The sign-in text message: SNS to the one phone number kept in Secrets Manager.

The number is never taken from a request. The only way to change where codes go
is to change the secret.
"""

import json
import logging
import re

from botocore.exceptions import ClientError

from .errors import ApiError
from .services import Services

log = logging.getLogger(__name__)

_E164 = re.compile(r"^\+[1-9][0-9]{6,14}$")


def _unavailable() -> ApiError:
    return ApiError(503, "sign_in_unavailable", "Sign-in is not set up. Put a phone number in the sign-in secret")


def _number_from(raw: str) -> str:
    """The secret is the number itself. A one-entry JSON object also works, which is what
    the console's key/value editor stores."""
    text = raw.strip()
    if text.startswith("{"):
        try:
            data = json.loads(text)
        except ValueError:
            return ""
        values = list(data.values()) if isinstance(data, dict) else []
        text = values[0].strip() if len(values) == 1 and isinstance(values[0], str) else ""
    return text


def normalise_number(raw: object) -> str:
    """What a person typed, as E.164, or a 400. Spaces, dashes, dots and brackets are dropped.

    This looks only at the input, never at the stored number, so a 400 says nothing
    about which number is right.
    """
    text = re.sub(r"[ \-.()]", "", raw) if isinstance(raw, str) else ""
    if not _E164.match(text):
        raise ApiError(400, "invalid_number", "Enter your phone number with the country code, like +61400000000")
    return text


def sign_in_number(svc: Services) -> str:
    """The phone number codes are sent to, in E.164 form such as +61400000000."""
    try:
        raw = svc.secrets.get_secret_value(SecretId=svc.settings.sign_in_secret)["SecretString"]
    except (ClientError, KeyError):
        log.exception("Could not read the sign-in secret")
        raise _unavailable() from None
    number = _number_from(raw)
    if not _E164.match(number):
        # The value is not logged: it is the number.
        log.error("The sign-in secret does not hold a phone number in E.164 format, such as +61400000000")
        raise _unavailable()
    return number


def send_code(svc: Services, number: str, otp: str, minutes: int) -> None:
    message = f"Launchpad sign-in code: {otp}. It works for {minutes} minutes. If you did not ask for it, ignore this message."
    try:
        svc.sns.publish(
            PhoneNumber=number,
            Message=message,
            MessageAttributes={"AWS.SNS.SMS.SMSType": {"DataType": "String", "StringValue": "Transactional"}},
        )
    except ClientError:
        # Do not tell the caller why: the reason can include account details.
        log.exception("Could not send the sign-in text message")
        raise ApiError(502, "sms_failed", "The sign-in code could not be sent. Try again shortly") from None
