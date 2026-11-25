"""CloudWatch metrics through the Embedded Metric Format.

Printing one JSON line per metric is enough: CloudWatch Logs turns it into a
metric in the `Launchpad` namespace, with no API call and no extra permission.
"""

import json
import time

NAMESPACE = "Launchpad"


def emit(name: str, value: float = 1, unit: str = "Count", **dimensions: str) -> None:
    record = {
        "_aws": {
            "Timestamp": int(time.time() * 1000),
            "CloudWatchMetrics": [
                {
                    "Namespace": NAMESPACE,
                    "Dimensions": [sorted(dimensions)] if dimensions else [[]],
                    "Metrics": [{"Name": name, "Unit": unit}],
                }
            ],
        },
        name: value,
        **dimensions,
    }
    print(json.dumps(record, separators=(",", ":")))
