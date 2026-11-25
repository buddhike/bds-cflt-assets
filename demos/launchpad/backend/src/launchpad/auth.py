"""Passwordless sign in: an emailed one-time code, then a session."""

import hashlib
import hmac
import secrets

from . import clock, mailer, metrics, users
from .db import ConditionFailed
from .errors import ApiError, bad_request, unauthorized
from .services import Services

OTP_MINUTES = 10
MAX_GUESSES = 5
RESEND_SECONDS = 30
MAX_SENDS_PER_HOUR = 10


def _otp_hash(salt: str, otp: str) -> str:
    return hmac.new(bytes.fromhex(salt), otp.encode(), hashlib.sha256).hexdigest()


def start(svc: Services, raw_email: object) -> None:
    """Email a one-time code. The answer is the same whether or not the user exists."""
    email = users.normalise_email(svc, raw_email)
    key = f"OTP#{email}"
    now = clock.now()
    previous = svc.store.get(key, "OTP") or {}

    if now - previous.get("sentAt", 0) < RESEND_SECONDS:
        raise ApiError(429, "too_soon", f"Wait {RESEND_SECONDS} seconds before asking for another code")
    hour_start, hour_count = previous.get("hourStart", now), previous.get("hourCount", 0)
    if now - hour_start >= 3600:
        hour_start, hour_count = now, 0
    if hour_count >= MAX_SENDS_PER_HOUR:
        raise ApiError(429, "too_many", "Too many codes requested. Try again later")

    otp = f"{secrets.randbelow(10**6):06d}"
    salt = secrets.token_hex(16)
    svc.store.put(
        {
            "pk": key,
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
    mailer.send_otp(svc, email, otp, OTP_MINUTES)
    metrics.emit("SignInStarted")


def verify(svc: Services, raw_email: object, raw_otp: object) -> dict:
    """Check the code. Returns {"status", "user", "token"?}; a token only for approved users."""
    email = users.normalise_email(svc, raw_email)
    otp = raw_otp.strip() if isinstance(raw_otp, str) else ""
    invalid = bad_request("invalid_code", "That code is wrong or has expired")
    if not (otp.isdigit() and len(otp) == 6):
        raise invalid

    key = f"OTP#{email}"
    item = svc.store.get(key, "OTP")
    if item is None or item["ttl"] <= clock.now():
        metrics.emit("SignInFailed")
        raise invalid
    try:
        # Count the guess before looking at it, so parallel guesses cannot beat the limit.
        item = svc.store.update(
            key,
            "OTP",
            "SET attempts = attempts + :one",
            {":one": 1, ":max": MAX_GUESSES},
            condition="attempts < :max",
        )
    except ConditionFailed:
        svc.store.delete(key, "OTP")
        metrics.emit("SignInFailed")
        raise invalid from None
    if not hmac.compare_digest(item["hash"], _otp_hash(item["salt"], otp)):
        metrics.emit("SignInFailed")
        raise invalid
    svc.store.delete(key, "OTP")

    user = users.register_or_get(svc, email)
    result: dict = {"status": user["status"], "user": users.public(user)}
    if user["status"] == users.APPROVED:
        result["token"] = _new_session(svc, email)
        metrics.emit("SignInSucceeded")
    return result


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
    """The signed-in, still approved user for a session token, or a 401."""
    if not token:
        raise unauthorized()
    item = svc.store.get(_session_key(token), "SESSION")
    if item is None or item["ttl"] <= clock.now():
        raise unauthorized("Your session has expired. Sign in again")
    user = users.get(svc, item["email"])
    # Checked on every request so that denying someone takes effect at once.
    if user is None or user["status"] != users.APPROVED:
        raise unauthorized("Your access is no longer approved")
    return user


def logout(svc: Services, token: str | None) -> None:
    if token:
        svc.store.delete(_session_key(token), "SESSION")
