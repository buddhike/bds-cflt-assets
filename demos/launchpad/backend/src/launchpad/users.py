"""The user store.

Sign-in is a code sent to one phone number, so there is one person who can sign
in: the owner, whose address is the `SuperUserEmail` setting. The address is what
names their saved credentials and the demos they launch.
"""

from . import clock, metrics
from .db import ConditionFailed
from .services import Services

ADMIN = "admin"


def pk(email: str) -> str:
    return f"USER#{email}"


def get(svc: Services, email: str) -> dict | None:
    return svc.store.get(pk(email), "PROFILE")


def ensure_owner(svc: Services) -> dict:
    """The owner's profile, created on their first sign-in."""
    email = svc.settings.super_user_email
    item = {"pk": pk(email), "sk": "PROFILE", "email": email, "role": ADMIN, "createdAt": clock.now()}
    try:
        svc.store.put(item, condition="attribute_not_exists(pk)")
    except ConditionFailed:
        return get(svc, email)  # type: ignore[return-value]
    metrics.emit("UserRegistered")
    return item


def public(user: dict) -> dict:
    return {"email": user["email"], "role": user["role"], "createdAt": user["createdAt"]}
