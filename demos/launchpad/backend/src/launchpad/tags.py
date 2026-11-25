"""The tags that identify Launchpad and what it deployed."""

MANAGED_KEY = "launchpad:managed"
DEMO_KEY = "launchpad:demo"


def demo_tags(demo_id: str) -> dict[str, str]:
    """Tags every resource of a demo must carry."""
    return {MANAGED_KEY: "true", DEMO_KEY: demo_id}
