"""Passwordless sign in: a one-time code sent by text message, then a session."""

import hashlib
import hmac
import secrets

from . import clock, metrics, sms, users
from .db import ConditionFailed
from .errors import ApiError, bad_request, unauthorized
from .services import Services

OTP_MINUTES = 10
MAX_GUESSES = 5
RESEND_SECONDS = 30
MAX_SENDS_PER_HOUR = 10

# There is one phone, so one code at a time.
_OTP_KEY = "OTP#owner"


def _otp_hash(salt: str, otp: str) -> str:
    return hmac.new(bytes.fromhex(salt), otp.encode(), hashlib.sha256).hexdigest()


def start(svc: Services, resend: bool = False) -> bool:
    """Text a one-time code to the owner's phone. Returns whether a message was sent.

    The page asks for a code whenever someone arrives without a session, so a code
    that is still good is left alone: otherwise anyone reloading the page, or
    anyone at all, would replace the code that is on the owner's phone.
    `resend` is the owner asking for a new one.
    """
    now = clock.now()
    previous = svc.store.get(_OTP_KEY, "OTP") or {}
    if not resend and previous and previous["ttl"] > now and previous["attempts"] < MAX_GUESSES:
        return False

    if now - previous.get("sentAt", 0) < RESEND_SECONDS:
        raise ApiError(429, "too_soon", f"Wait {RESEND_SECONDS} seconds before asking for another code")
    hour_start, hour_count = previous.get("hourStart", now), previous.get("hourCount", 0)
    if now - hour_start >= 3600:
        hour_start, hour_count = now, 0
    if hour_count >= MAX_SENDS_PER_HOUR:
        raise ApiError(429, "too_many", "Too many codes requested. Try again later")

    # Read the number first, so that a missing one does not use up a send.
    number = sms.sign_in_number(svc)
    otp = f"{secrets.randbelow(10**6):06d}"
    salt = secrets.token_hex(16)
    svc.store.put(
        {
            "pk": _OTP_KEY,
            "sk": "OTP",
            "salt": salt,
            "hash": _otp_hash(salt, otp),
            "attempts": 0,
            "sentAt": now,
            "hourStart": hour_start,
            "hourCount": hour_count + 1,
            "ttl": now + OTP_MINUTES * 60,
        }
    )
    sms.send_code(svc, number, otp, OTP_MINUTES)
    metrics.emit("SignInStarted")
    return True


def verify(svc: Services, raw_otp: object) -> dict:
    """Check the code. Returns {"user", "token"}: the owner, signed in."""
    otp = raw_otp.strip() if isinstance(raw_otp, str) else ""
    invalid = bad_request("invalid_code", "That code is wrong or has expired")
    if not (otp.isdigit() and len(otp) == 6):
        raise invalid

    item = svc.store.get(_OTP_KEY, "OTP")
    if item is None or item["ttl"] <= clock.now():
        metrics.emit("SignInFailed")
        raise invalid
    try:
        # Count the guess before looking at it, so parallel guesses cannot beat the limit.
        item = svc.store.update(
            _OTP_KEY,
            "OTP",
            "SET attempts = attempts + :one",
            {":one": 1, ":max": MAX_GUESSES},
            condition="attempts < :max",
        )
    except ConditionFailed:
        svc.store.delete(_OTP_KEY, "OTP")
        metrics.emit("SignInFailed")
        raise invalid from None
    if not hmac.compare_digest(item["hash"], _otp_hash(item["salt"], otp)):
        metrics.emit("SignInFailed")
        raise invalid
    svc.store.delete(_OTP_KEY, "OTP")

    user = users.ensure_owner(svc)
    metrics.emit("SignInSucceeded")
    return {"user": users.public(user), "token": _new_session(svc, user["email"])}


def _session_key(token: str) -> str:
    return "SESSION#" + hashlib.sha256(token.encode()).hexdigest()


def _new_session(svc: Services, email: str) -> str:
    token = secrets.token_urlsafe(32)
    svc.store.put(
        {
            "pk": _session_key(token),
            "sk": "SESSION",
            "email": email,
            "ttl": clock.now() + svc.settings.session_hours * 3600,
        }
    )
    return token


def session_user(svc: Services, token: str | None) -> dict:
    """The signed-in owner for a session token, or a 401."""
    if not token:
        raise unauthorized()
    item = svc.store.get(_session_key(token), "SESSION")
    if item is None or item["ttl"] <= clock.now():
        raise unauthorized("Your session has expired. Sign in again")
    user = users.get(svc, item["email"])
    # Checked on every request, so changing SuperUserEmail ends the old owner's sessions.
    if user is None or user["email"] != svc.settings.super_user_email:
        raise unauthorized("Your session has expired. Sign in again")
    return user


def logout(svc: Services, token: str | None) -> None:
    if token:
        svc.store.delete(_session_key(token), "SESSION")
