import pytest

from launchpad import source
from launchpad.errors import ApiError

from fakes import SHA, Resp, tar_gz


def serve(svc, members):
    svc.http = lambda req, timeout=None: Resp(tar_gz(members))


def test_extracts_only_the_demo(svc, tmp_path):
    serve(svc, [
        (f"repo-{SHA}/", None, "dir"),
        (f"repo-{SHA}/README.md", b"x", "file"),
        (f"repo-{SHA}/demos/pims/tf/main.tf", b"resource", "file"),
        (f"repo-{SHA}/demos/pims/tf/run.sh", b"#!/bin/sh", "file"),
        (f"repo-{SHA}/demos/other/tf/main.tf", b"nope", "file"),
        (f"repo-{SHA}/demos/pimsx/tf/main.tf", b"nope", "file"),
    ])
    out = source.download(svc, SHA, "demos/pims", tmp_path / "w")
    assert (out / "tf/main.tf").read_text() == "resource"
    assert not (out / "README.md").exists() and not (out / "other").exists() and not (out / "x").exists()
    assert source.read_tf(out) == {"main.tf": "resource"}


def test_hostile_archive_cannot_write_outside(svc, tmp_path):
    serve(svc, [
        (f"repo-{SHA}/demos/pims/tf/main.tf", b"ok", "file"),
        (f"repo-{SHA}/demos/pims/../../../evil.txt", b"evil", "file"),
        (f"repo-{SHA}/demos/pims/tf/link", b"/etc/passwd", "symlink"),
        (f"repo-{SHA}/demos/pims//abs", b"x", "file"),
    ])
    out = source.download(svc, SHA, "demos/pims", tmp_path / "w")
    assert not (tmp_path / "evil.txt").exists() and not (tmp_path.parent / "evil.txt").exists()
    assert not (out / "tf/link").exists()
    assert (out / "tf/main.tf").exists()


def test_missing_tf_directory_is_an_error(svc, tmp_path):
    serve(svc, [(f"repo-{SHA}/demos/pims/main.go", b"x", "file")])
    with pytest.raises(ApiError) as e:
        source.download(svc, SHA, "demos/pims", tmp_path / "w")
    assert e.value.code == "demo_missing"


def test_download_replaces_a_previous_copy(svc, tmp_path):
    serve(svc, [(f"repo-{SHA}/demos/pims/tf/new.tf", b"n", "file")])
    dest = tmp_path / "w"
    (dest / "tf").mkdir(parents=True)
    (dest / "tf/old.tf").write_text("o")
    source.download(svc, SHA, "demos/pims", dest)
    assert not (dest / "tf/old.tf").exists() and (dest / "tf/new.tf").exists()
