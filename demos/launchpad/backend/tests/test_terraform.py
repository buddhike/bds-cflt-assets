import hashlib
import io
import stat
import time
import zipfile

import pytest

from launchpad import terraform
from launchpad.errors import ApiError


def fake_binary(tmp_path, body: str):
    p = tmp_path / "terraform"
    p.write_text("#!/bin/bash\n" + body)
    p.chmod(p.stat().st_mode | stat.S_IEXEC)
    return p


def tf(tmp_path, body, secrets=()):
    log = terraform.LogTail(list(secrets))
    return terraform.Terraform(fake_binary(tmp_path, body), tmp_path, {"PATH": "/usr/bin:/bin"}, log), log


def test_output_and_exit_code(tmp_path):
    t, _ = tf(tmp_path, 'echo "hello $1"; echo oops >&2; exit 3')
    r = t.run("world")
    assert r.code == 3 and not r.interrupted
    assert "hello world" in r.output and "oops" in r.output


def test_secrets_are_removed_from_the_log(tmp_path):
    t, log = tf(tmp_path, 'echo "token is s3cr3t-value here"', secrets=["s3cr3t-value", "ab"])
    r = t.run()
    assert "s3cr3t-value" not in r.output and "token is *** here" in r.output


def test_deadline_interrupts_and_terraform_can_exit_cleanly(tmp_path):
    t, _ = tf(tmp_path, 'trap "echo Interrupt received; exit 1" INT\nwhile true; do sleep 0.1; done')
    r = t.run("apply", deadline=time.time() + 1.2, grace=5)
    assert r.interrupted and r.code == 1 and "Interrupt received" in r.output


def test_a_process_that_ignores_the_interrupt_is_killed(tmp_path):
    t, _ = tf(tmp_path, 'trap "" INT\nwhile true; do sleep 0.1; done')
    start = time.time()
    r = t.run("apply", deadline=time.time() + 1.0, grace=1.5)
    assert r.interrupted and r.code != 0
    assert time.time() - start < 15


def test_a_run_that_finishes_before_the_deadline_is_not_interrupted(tmp_path):
    t, _ = tf(tmp_path, "exit 0")
    assert not t.run(deadline=time.time() + 30).interrupted


def test_progress_callback_is_called_while_running(tmp_path, monkeypatch):
    calls = []
    log = terraform.LogTail([])
    t = terraform.Terraform(fake_binary(tmp_path, "sleep 12"), tmp_path, {"PATH": "/usr/bin:/bin"}, log, on_progress=lambda: calls.append(1))
    t.run()
    assert calls


def test_binary_download_checks_the_checksum(svc, tmp_path, monkeypatch):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("terraform", "#!/bin/sh\necho hi\n")
    data = buf.getvalue()

    class R(io.BytesIO):
        def __enter__(self): return self
        def __exit__(self, *a): pass

    svc.http = lambda req, timeout=None: R(data)
    monkeypatch.setattr(terraform, "CHECKSUMS", {"amd64": "0" * 64, "arm64": "0" * 64})
    with pytest.raises(ApiError):
        terraform.ensure_binary(svc, tmp_path / "bin")
    digest = hashlib.sha256(data).hexdigest()
    monkeypatch.setattr(terraform, "CHECKSUMS", {"amd64": digest, "arm64": digest})
    path = terraform.ensure_binary(svc, tmp_path / "bin")
    assert path.exists() and path.stat().st_mode & stat.S_IXUSR
    svc.http = lambda *a, **k: (_ for _ in ()).throw(AssertionError("downloaded twice"))
    assert terraform.ensure_binary(svc, tmp_path / "bin") == path
