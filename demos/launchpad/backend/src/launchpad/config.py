"""Settings, read once from the Lambda environment."""

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Settings:
    table_name: str
    kms_key_id: str
    state_bucket: str
    region: str
    super_user_email: str
    demos_repo: str  # owner/name
    demos_path: str  # directory in the repository that holds the demos
    demos_ref: str  # branch or tag the demos are read from
    github_token: str
    runner_function: str
    sign_in_secret: str  # ARN of the secret that holds the phone number codes go to
    session_hours: int = 12

    @classmethod
    def from_env(cls) -> "Settings":
        env = os.environ
        return cls(
            table_name=env["TABLE_NAME"],
            kms_key_id=env["KMS_KEY_ID"],
            state_bucket=env.get("STATE_BUCKET", ""),
            region=env.get("AWS_REGION", "us-east-1"),
            super_user_email=env["SUPER_USER_EMAIL"].strip().lower(),
            demos_repo=env.get("DEMOS_REPO", "buddhike/bds-cflt-assets"),
            demos_path=env.get("DEMOS_PATH", "demos").strip("/"),
            demos_ref=env.get("DEMOS_REF", "main"),
            github_token=env.get("GITHUB_TOKEN", ""),
            runner_function=env.get("RUNNER_FUNCTION", ""),
            sign_in_secret=env.get("SIGN_IN_SECRET", ""),
            session_hours=int(env.get("SESSION_HOURS", "12")),
        )
