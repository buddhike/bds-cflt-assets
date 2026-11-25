"""Each user's Confluent Cloud API key, kept encrypted.

The key and secret are encrypted with KMS before they reach DynamoDB, bound to
the user's email through the encryption context, so a copy of one user's blob
under another user's name does not decrypt. Only the runner decrypts them.
The API only ever says whether a key is saved and shows the last four
characters of its id.
"""

import json
from dataclasses import dataclass

from . import clock, metrics
from .errors import bad_request
from .services import Services

MAX_LENGTH = 200


@dataclass(frozen=True)
class Credentials:
    api_key: str
    api_secret: str

    def __repr__(self) -> str:  # never print the secret by accident
        return f"Credentials(api_key={self.api_key[:4]}***, api_secret=***)"


def _context(email: str) -> dict[str, str]:
    return {"launchpad:user": email}


def _clean(name: str, value: object) -> str:
    if not isinstance(value, str) or not value.strip():
        raise bad_request("invalid_credentials", f"Enter the {name}")
    value = value.strip()
    if len(value) > MAX_LENGTH or any(c.isspace() for c in value):
        raise bad_request("invalid_credentials", f"The {name} is not valid")
    return value


def save(svc: Services, email: str, api_key: object, api_secret: object) -> dict:
    creds = Credentials(_clean("API key", api_key), _clean("API secret", api_secret))
    blob = svc.kms.encrypt(
        KeyId=svc.settings.kms_key_id,
        Plaintext=json.dumps({"key": creds.api_key, "secret": creds.api_secret}).encode(),
        EncryptionContext=_context(email),
    )["CiphertextBlob"]
    svc.store.put(
        {
            "pk": f"USER#{email}",
            "sk": "CREDS",
            "ciphertext": blob,
            "apiKeyHint": creds.api_key[-4:],
            "updatedAt": clock.now(),
        }
    )
    metrics.emit("CredentialsSaved")
    return status(svc, email)


def status(svc: Services, email: str) -> dict:
    item = svc.store.get(f"USER#{email}", "CREDS")
    if item is None:
        return {"configured": False}
    return {"configured": True, "apiKeyHint": item["apiKeyHint"], "updatedAt": item["updatedAt"]}


def delete(svc: Services, email: str) -> None:
    svc.store.delete(f"USER#{email}", "CREDS")


def load(svc: Services, email: str) -> Credentials | None:
    """The decrypted credentials, for the runner."""
    item = svc.store.get(f"USER#{email}", "CREDS")
    if item is None:
        return None
    plain = svc.kms.decrypt(
        CiphertextBlob=bytes(item["ciphertext"]),  # boto3 hands back a Binary
        KeyId=svc.settings.kms_key_id,
        EncryptionContext=_context(email),
    )["Plaintext"]
    data = json.loads(plain)
    return Credentials(data["key"], data["secret"])
