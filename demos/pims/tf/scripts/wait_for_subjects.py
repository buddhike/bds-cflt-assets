#!/usr/bin/env python3
"""Wait until Schema Registry has a "<topic>-value" subject for every topic.

Debezium registers a topic's schema with the topic's first record, and Tableflow
cannot be enabled on a topic without one. Terraform runs this between starting the
app, which makes the records, and enabling Tableflow.

Settings come from the environment, so no secret is on a command line:
  SCHEMA_REGISTRY_URL, SCHEMA_REGISTRY_KEY, SCHEMA_REGISTRY_SECRET, TOPICS (comma
  separated), TIMEOUT_SECONDS (default 1500), POLL_SECONDS (default 10).

Standard library only: it runs in Launchpad's Lambda, which has Python and nothing else.
"""

import base64
import json
import os
import sys
import time
import urllib.error
import urllib.request


def subjects(url: str, key: str, secret: str) -> list[str]:
    req = urllib.request.Request(url.rstrip("/") + "/subjects")
    token = base64.b64encode(f"{key}:{secret}".encode()).decode()
    req.add_header("Authorization", f"Basic {token}")
    req.add_header("User-Agent", "pims-launchpad")
    with urllib.request.urlopen(req, timeout=20) as resp:
        return json.load(resp)


def missing(topics: list[str], have: list[str]) -> list[str]:
    return [f"{t}-value" for t in topics if f"{t}-value" not in have]


def wait(url, key, secret, topics, timeout, poll, now=time.monotonic, sleep=time.sleep, fetch=subjects) -> bool:
    deadline = now() + timeout
    while True:
        try:
            left = missing(topics, fetch(url, key, secret))
            if not left:
                print("All topic schemas are registered.", flush=True)
                return True
            print(f"Waiting for Debezium to register: {', '.join(left)}", flush=True)
        except (urllib.error.URLError, OSError, ValueError) as e:
            # A bad key is worth saying, but not worth stopping for: keys take a moment to work.
            reason = getattr(e, "code", None) or type(e).__name__
            print(f"Could not read Schema Registry ({reason}), will try again", flush=True)
        if now() >= deadline:
            return False
        sleep(poll)


def main() -> int:
    env = os.environ
    try:
        url, key, secret = env["SCHEMA_REGISTRY_URL"], env["SCHEMA_REGISTRY_KEY"], env["SCHEMA_REGISTRY_SECRET"]
        topics = [t.strip() for t in env["TOPICS"].split(",") if t.strip()]
    except KeyError as e:
        print(f"{e.args[0]} is not set", file=sys.stderr)
        return 2
    if not url.startswith("https://") or not topics:
        print("SCHEMA_REGISTRY_URL must be https and TOPICS must not be empty", file=sys.stderr)
        return 2
    timeout = float(env.get("TIMEOUT_SECONDS", "1500"))
    if wait(url, key, secret, topics, timeout, float(env.get("POLL_SECONDS", "10"))):
        return 0
    print(f"Gave up after {int(timeout)} seconds. Is the app running and is the connector healthy?", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
