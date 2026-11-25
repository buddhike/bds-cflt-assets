"""Running the Terraform binary inside Lambda."""

import hashlib
import io
import os
import platform
import shutil
import signal
import subprocess
import threading
import time
import urllib.request
import zipfile
from collections import deque
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from .errors import ApiError
from .services import Services

VERSION = "1.16.3"
# SHA-256 of the release zips, from releases.hashicorp.com/terraform/<VERSION>/..._SHA256SUMS.
CHECKSUMS = {
    "amd64": "093b6ae9a2228af5029c41606bc96eb583553528aad1bfe7e0b4d62fc91e25d8",
    "arm64": "b6b293550af7a6a7ffd867a04900ce161db86ec9407b234a636c2adbae2ca9f0",
}
# Terraform gets this long to stop after an interrupt before it is killed.
STOP_GRACE_SECONDS = 90


def _arch() -> str:
    return {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine().lower()]


def ensure_binary(svc: Services, directory: Path) -> Path:
    """Download Terraform once per container, checking its checksum."""
    binary = directory / "terraform"
    if binary.exists():
        return binary
    arch = _arch()
    url = f"https://releases.hashicorp.com/terraform/{VERSION}/terraform_{VERSION}_linux_{arch}.zip"
    opener = svc.http or urllib.request.urlopen
    with opener(urllib.request.Request(url, headers={"User-Agent": "launchpad"}), timeout=120) as r:
        data = r.read(200 * 1024 * 1024)
    if hashlib.sha256(data).hexdigest() != CHECKSUMS[arch]:
        raise ApiError(500, "terraform_checksum", "The downloaded Terraform did not match its checksum")
    directory.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        with z.open("terraform") as src, open(binary, "wb") as out:
            shutil.copyfileobj(src, out)
    binary.chmod(0o755)
    return binary


class LogTail:
    """The last lines of output, with secrets removed."""

    def __init__(self, secrets: list[str], max_lines: int = 200, max_chars: int = 30000):
        self._secrets = [s for s in secrets if len(s) >= 4]
        self._lines: deque[str] = deque(maxlen=max_lines)
        self._max_chars = max_chars
        self._lock = threading.Lock()

    def add(self, line: str) -> None:
        for s in self._secrets:
            line = line.replace(s, "***")
        with self._lock:
            self._lines.append(line.rstrip("\n"))

    def text(self) -> str:
        with self._lock:
            out = "\n".join(self._lines)
        return out[-self._max_chars :]


@dataclass
class Result:
    code: int
    interrupted: bool  # the deadline passed and Terraform was told to stop
    output: str  # the tail of the output


class Terraform:
    def __init__(self, binary: Path, workdir: Path, env: dict[str, str], log: LogTail, on_progress: Callable[[], None] | None = None):
        self.binary, self.workdir, self.env, self.log = binary, workdir, env, log
        self._progress = on_progress

    def run(self, *args: str, deadline: float | None = None, grace: float = STOP_GRACE_SECONDS) -> Result:
        """Run one Terraform command. At `deadline` (a time.time() value) it is interrupted."""
        proc = subprocess.Popen(
            [str(self.binary), *args],
            cwd=self.workdir,
            env=self.env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            errors="replace",
            start_new_session=True,
        )
        done = threading.Event()
        interrupted = threading.Event()

        def watchdog() -> None:
            last_progress = time.time()
            while not done.wait(1.0):
                now = time.time()
                if self._progress and now - last_progress >= 10:
                    last_progress = now
                    self._progress()
                if deadline is not None and now >= deadline and not interrupted.is_set():
                    interrupted.set()
                    self.log.add("Launchpad: time is nearly up, asking Terraform to stop")
                    proc.send_signal(signal.SIGINT)
                    if done.wait(grace):
                        return
                    self.log.add("Launchpad: Terraform did not stop in time, killing it")
                    os.killpg(proc.pid, signal.SIGKILL)
                    return

        t = threading.Thread(target=watchdog, daemon=True)
        t.start()
        assert proc.stdout is not None
        for line in proc.stdout:
            self.log.add(line)
        code = proc.wait()
        done.set()
        t.join()
        return Result(code=code, interrupted=interrupted.is_set(), output=self.log.text())
