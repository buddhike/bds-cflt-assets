"""Email, through SES."""

import logging

from botocore.exceptions import ClientError

from .errors import ApiError
from .services import Services

log = logging.getLogger(__name__)


def send_otp(svc: Services, email: str, otp: str, minutes: int) -> None:
    body = (
        f"Your Launchpad sign-in code is {otp}\n\n"
        f"It works for {minutes} minutes. If you did not ask for it, ignore this email.\n"
    )
    try:
        svc.ses.send_email(
            Source=svc.settings.sender_email,
            Destination={"ToAddresses": [email]},
            Message={
                "Subject": {"Data": "Your Launchpad sign-in code", "Charset": "UTF-8"},
                "Body": {"Text": {"Data": body, "Charset": "UTF-8"}},
            },
        )
    except ClientError:
        # Do not tell the caller why: the reason can include account details.
        log.exception("Could not send the sign-in email")
        raise ApiError(502, "email_failed", "The sign-in email could not be sent. Try again shortly") from None
