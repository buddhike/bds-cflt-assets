"""Download a demo's Terraform from the repository."""

import os
import shutil
import tarfile
import tempfile
from pathlib import Path

from . import github
from .errors import ApiError
from .services import Services


def download(svc: Services, sha: str, demo_path: str, dest: Path) -> Path:
    """Extract <demo_path>/ of the repository at sha into dest and return dest.

    Only regular files and directories under demo_path are written, whatever
    else the archive holds, so a hostile archive cannot write elsewhere.
    """
    if dest.exists():
        shutil.rmtree(dest)
    dest.mkdir(parents=True)
    with tempfile.TemporaryFile() as tmp, github.tarball(svc, sha) as resp:
        shutil.copyfileobj(resp, tmp, 1024 * 1024)
        tmp.seek(0)
        with tarfile.open(fileobj=tmp, mode="r:gz") as tar:
            _extract(tar, demo_path, dest)
    if not (dest / "tf").is_dir():
        raise ApiError(502, "demo_missing", f"{demo_path}/tf is not in the repository at this version")
    return dest


def _extract(tar: tarfile.TarFile, demo_path: str, dest: Path) -> None:
    root = dest.resolve()
    prefix = demo_path.strip("/") + "/"
    for member in tar:
        # GitHub archives hold one top directory named <repo>-<sha>.
        _, _, rel = member.name.partition("/")
        if not rel.startswith(prefix):
            continue
        rel = rel[len(prefix) :]
        if not rel or not (member.isfile() or member.isdir()):
            continue
        target = (root / rel).resolve()
        if root != target and root not in target.parents:
            continue
        if member.isdir():
            target.mkdir(parents=True, exist_ok=True)
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        src = tar.extractfile(member)
        if src is None:
            continue
        with src, open(target, "wb") as out:
            shutil.copyfileobj(src, out)
        os.chmod(target, 0o755 if member.mode & 0o111 else 0o644)


def read_tf(demo_dir: Path) -> dict[str, str]:
    """The top-level .tf files of a downloaded demo, for the interface check."""
    return {p.name: p.read_text() for p in sorted((demo_dir / "tf").glob("*.tf"))}
