"""Lambda entry point for the runner. Invoked asynchronously by the API and by itself."""

from .. import runner
from ..services import Services

_svc: Services | None = None


def handler(event, context):
    global _svc
    if _svc is None:
        _svc = Services.from_env()
    result = runner.handler_run(_svc, event, lambda: context.get_remaining_time_in_millis() / 1000)
    return {"result": result}
