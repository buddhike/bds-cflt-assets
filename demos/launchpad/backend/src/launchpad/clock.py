"""Time, in one place so tests can move it."""

import time


def now() -> int:
    """Seconds since the epoch."""
    return int(time.time())
