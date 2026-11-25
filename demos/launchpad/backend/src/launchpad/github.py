"""Read-only access to the repository the demos live in."""

import json
import re
import urllib.error
import urllib.request

from .errors import ApiError
from .services import Services

_REPO = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
_REF = re.compile(r"^[A-Za-z0-9_./-]{1,200}$")
MAX_BYTES = 200 * 1024 * 1024


def _open(svc: Services, url: str, headers: dict[str, str] | None = None, timeout: int = 20):
    req = urllib.request.Request(url, headers={"User-Agent": "launchpad", **(headers or {})})
    if svc.settings.github_token and url.startswith("https://api.github.com/"):
        req.add_header("Authorization", f"Bearer {svc.settings.github_token}")
    opener = svc.http or urllib.request.urlopen
    try:
        return opener(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        if e.code in (403, 429):
            raise ApiError(502, "github_rate_limited", "GitHub is limiting requests. Try again later") from None
        if e.code == 404:
            raise ApiError(502, "github_not_found", "The demos repository or path was not found on GitHub") from None
        raise ApiError(502, "github_error", f"GitHub answered {e.code}") from None
    except urllib.error.URLError:
        raise ApiError(502, "github_unreachable", "GitHub could not be reached") from None


def _repo(svc: Services) -> str:
    repo = svc.settings.demos_repo
    if not _REPO.match(repo):
        raise ValueError(f"invalid repository {repo!r}")
    return repo


def resolve_ref(svc: Services) -> str:
    """The commit SHA the configured branch or tag points at now."""
    ref = svc.settings.demos_ref
    if not _REF.match(ref):
        raise ValueError(f"invalid ref {ref!r}")
    with _open(svc, f"https://api.github.com/repos/{_repo(svc)}/commits/{ref}", {"Accept": "application/vnd.github.sha"}) as r:
        sha = r.read(100).decode().strip()
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ApiError(502, "github_error", "GitHub returned an unexpected commit id")
    return sha


def tree(svc: Services, sha: str) -> list[dict]:
    with _open(svc, f"https://api.github.com/repos/{_repo(svc)}/git/trees/{sha}?recursive=1") as r:
        data = json.loads(r.read(MAX_BYTES))
    if data.get("truncated"):
        raise ApiError(502, "github_truncated", "The repository is too large to list its demos")
    return data["tree"]


def raw(svc: Services, sha: str, path: str) -> str:
    with _open(svc, f"https://raw.githubusercontent.com/{_repo(svc)}/{sha}/{path}") as r:
        return r.read(1024 * 1024).decode("utf-8", "replace")


def tarball(svc: Services, sha: str):
    """An open response with the repository at sha as a gzip tarball."""
    return _open(svc, f"https://codeload.github.com/{_repo(svc)}/tar.gz/{sha}", timeout=120)
