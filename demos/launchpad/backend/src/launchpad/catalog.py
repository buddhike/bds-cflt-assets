"""The list of demos, discovered from the repository and cached in DynamoDB."""

import json
import logging
import re

from . import clock, github, interface
from .services import Services

log = logging.getLogger(__name__)

CACHE_SECONDS = 600
# A demo id ends up in tags, state keys and AWS names, so keep it plain.
ID = re.compile(r"^[a-z0-9][a-z0-9-]{0,30}$")


def refresh(svc: Services) -> list[dict]:
    """Re-read the repository and replace the cached catalog."""
    sha = github.resolve_ref(svc)
    entries = github.tree(svc, sha)
    base = svc.settings.demos_path + "/"
    paths = {e["path"]: e["type"] for e in entries}

    demos = []
    for path, kind in sorted(paths.items()):
        if kind != "tree" or not path.startswith(base):
            continue
        demo_id = path[len(base) :]
        if "/" in demo_id or paths.get(f"{path}/tf") != "tree":
            continue
        if not ID.match(demo_id):
            log.warning("Skipping a demo whose directory name is not a valid id: %s", demo_id)
            continue
        demos.append(_describe(svc, sha, demo_id, path, paths))

    now = clock.now()
    wanted = {f"DEMO#{d['id']}" for d in demos}
    for old in svc.store.query("CATALOG", "DEMO#"):
        if old["sk"] not in wanted:
            svc.store.delete("CATALOG", old["sk"])
    for d in demos:
        svc.store.put({"pk": "CATALOG", "sk": f"DEMO#{d['id']}", **d})
    svc.store.put({"pk": "CATALOG", "sk": "META", "refreshedAt": now, "ref": sha})
    return demos


def _describe(svc: Services, sha: str, demo_id: str, path: str, paths: dict[str, str]) -> dict:
    meta: dict = {}
    if paths.get(f"{path}/demo.json") == "blob":
        try:
            loaded = json.loads(github.raw(svc, sha, f"{path}/demo.json"))
            meta = loaded if isinstance(loaded, dict) else {}
        except ValueError:
            log.warning("Ignoring demo.json that is not valid JSON: %s", path)
    tf_files = {
        p.rsplit("/", 1)[1]: github.raw(svc, sha, p)
        for p, kind in paths.items()
        if kind == "blob" and p.startswith(f"{path}/tf/") and p.endswith(".tf") and "/" not in p[len(path) + 4 :]
    }
    iface = interface.check(tf_files)
    return {
        "id": demo_id,
        "name": str(meta.get("name") or demo_id)[:80],
        "description": str(meta.get("description") or "")[:500],
        "path": path,
        "ref": sha,
        "valid": iface.valid,
        "problems": iface.problems,
        "needsConfluent": iface.needs_confluent,
    }


def _public(item: dict) -> dict:
    return {k: v for k, v in item.items() if k not in ("pk", "sk")}


def list_demos(svc: Services, force: bool = False) -> list[dict]:
    """The cached catalog, refreshed when it is older than CACHE_SECONDS."""
    meta = svc.store.get("CATALOG", "META")
    stale = meta is None or clock.now() - meta["refreshedAt"] >= CACHE_SECONDS
    if force or stale:
        try:
            return refresh(svc)
        except Exception:
            if force or meta is None:
                raise
            log.exception("Could not refresh the demo catalog, using the cached one")
    return [_public(i) for i in svc.store.query("CATALOG", "DEMO#")]


def get(svc: Services, demo_id: str) -> dict | None:
    if not ID.match(demo_id):
        return None
    if svc.store.get("CATALOG", "META") is None:
        list_demos(svc)  # nobody has listed the demos yet
    item = svc.store.get("CATALOG", f"DEMO#{demo_id}")
    return _public(item) if item else None
