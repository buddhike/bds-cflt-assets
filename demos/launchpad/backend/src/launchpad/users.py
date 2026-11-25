"""The user store: who has signed up, and whether an admin let them in."""

import re

from . import clock, metrics
from .db import ConditionFailed
from .errors import bad_request, forbidden, not_found
from .services import Services

PENDING, APPROVED, DENIED = "pending", "approved", "denied"
ADMIN, USER = "admin", "user"

_EMAIL = re.compile(r"^[^@\s]+@[^@\s]+$")


def pk(email: str) -> str:
    return f"USER#{email}"


def normalise_email(svc: Services, raw: object) -> str:
    """The canonical form of an address, or a 400 if it may not sign up."""
    if not isinstance(raw, str):
        raise bad_request("invalid_email", "Enter your email address")
    email = raw.strip().lower()
    if len(email) > 254 or not _EMAIL.match(email):
        raise bad_request("invalid_email", "Enter a valid email address")
    domain = email.rsplit("@", 1)[1]
    if domain != svc.settings.allowed_email_domain:
        raise bad_request("invalid_domain", f"Use your @{svc.settings.allowed_email_domain} email address")
    return email


def get(svc: Services, email: str) -> dict | None:
    return svc.store.get(pk(email), "PROFILE")


def register_or_get(svc: Services, email: str) -> dict:
    """Return the user, creating them on first sight.

    The super user is approved at once. Everybody else waits for review.
    """
    is_super = email == svc.settings.super_user_email
    status = APPROVED if is_super else PENDING
    created = clock.now()
    item = {
        "pk": pk(email),
        "sk": "PROFILE",
        "email": email,
        "status": status,
        "role": ADMIN if is_super else USER,
        "createdAt": created,
        "gsi1pk": f"STATUS#{status}",
        "gsi1sk": created,
    }
    try:
        svc.store.put(item, condition="attribute_not_exists(pk)")
    except ConditionFailed:
        return get(svc, email)  # type: ignore[return-value]
    metrics.emit("UserRegistered", status=status)
    return item


def public(user: dict) -> dict:
    return {
        "email": user["email"],
        "status": user["status"],
        "role": user["role"],
        "createdAt": user["createdAt"],
        "decidedBy": user.get("decidedBy"),
        "decidedAt": user.get("decidedAt"),
    }


def list_requests(svc: Services) -> list[dict]:
    return [public(u) for u in svc.store.query_index(f"STATUS#{PENDING}")]


def list_all(svc: Services) -> list[dict]:
    users = []
    for status in (PENDING, APPROVED, DENIED):
        users.extend(public(u) for u in svc.store.query_index(f"STATUS#{status}"))
    return sorted(users, key=lambda u: u["createdAt"])


def decide(svc: Services, actor: str, email: str, approve: bool) -> dict:
    """Approve or deny a user. Admins cannot be changed, so the last one cannot be locked out."""
    user = get(svc, email)
    if user is None:
        raise not_found("No such user")
    if user["role"] == ADMIN:
        raise forbidden("An admin's access cannot be changed")
    status = APPROVED if approve else DENIED
    updated = svc.store.update(
        pk(email),
        "PROFILE",
        "SET #s = :s, gsi1pk = :g, decidedBy = :by, decidedAt = :at",
        {":s": status, ":g": f"STATUS#{status}", ":by": actor, ":at": clock.now()},
        names={"#s": "status"},
        condition="attribute_exists(pk)",
    )
    metrics.emit("UserDecided", decision=status)
    return public(updated)
