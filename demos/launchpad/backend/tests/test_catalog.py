import json

import pytest

from launchpad import catalog, clock
from launchpad.errors import ApiError

from fakes import CC_TF, GOOD_TF, SHA, FakeGitHub

FILES = {
    "demos/pims/tf/main.tf": CC_TF,
    "demos/pims/demo.json": json.dumps({"name": "PIMS", "description": "Clinic CDC demo"}),
    "demos/pims/main.go": "package main",
    "demos/plain/tf/main.tf": GOOD_TF,
    "demos/broken/tf/main.tf": "resource \"x\" \"y\" {}",
    "demos/launchpad/backend/x.py": "",  # not a demo: no tf directory
    "demos/Bad_Name/tf/main.tf": GOOD_TF,  # not a valid id
    "demos/pims/tf/modules/m.tf": 'variable "aws_region" {}',  # nested, ignored
    "README.md": "",
}


@pytest.fixture
def gh(svc):
    fake = FakeGitHub()
    fake.add_repo(FILES)
    svc.http = fake
    return fake


def test_refresh_discovers_demos_with_tf(svc, gh):
    demos = {d["id"]: d for d in catalog.refresh(svc)}
    assert set(demos) == {"pims", "plain", "broken"}
    assert demos["pims"]["name"] == "PIMS" and demos["pims"]["description"] == "Clinic CDC demo"
    assert demos["pims"]["needsConfluent"] and demos["pims"]["valid"]
    assert demos["plain"]["name"] == "plain" and not demos["plain"]["needsConfluent"]
    assert not demos["broken"]["valid"] and demos["broken"]["problems"]
    assert demos["pims"]["ref"] == SHA and demos["pims"]["path"] == "demos/pims"


def test_demos_path_is_configurable(svc, gh):
    gh.add_repo({"launchpad-demos/x/tf/main.tf": GOOD_TF})
    svc.settings = svc.settings.__class__(**{**svc.settings.__dict__, "demos_path": "launchpad-demos"})
    assert [d["id"] for d in catalog.refresh(svc)] == ["x"]


def test_removed_demos_leave_the_catalog(svc, gh):
    catalog.refresh(svc)
    gh.add_repo({"demos/plain/tf/main.tf": GOOD_TF})
    assert [d["id"] for d in catalog.refresh(svc)] == ["plain"]
    assert catalog.get(svc, "pims") is None


def test_list_uses_the_cache_until_it_is_stale(svc, gh, now):
    catalog.list_demos(svc)
    calls = len(gh.calls)
    catalog.list_demos(svc)
    assert len(gh.calls) == calls
    now.t += catalog.CACHE_SECONDS
    catalog.list_demos(svc)
    assert len(gh.calls) > calls


def test_stale_cache_survives_a_github_outage(svc, gh, now):
    catalog.list_demos(svc)
    now.t += catalog.CACHE_SECONDS
    gh.routes.clear()
    assert {d["id"] for d in catalog.list_demos(svc)} == {"pims", "plain", "broken"}
    with pytest.raises(ApiError):
        catalog.list_demos(svc, force=True)


def test_first_load_with_github_down_is_an_error(svc, gh):
    gh.routes.clear()
    with pytest.raises(ApiError) as e:
        catalog.list_demos(svc)
    assert e.value.status == 502


def test_get_rejects_ids_that_are_not_ids(svc, gh):
    catalog.refresh(svc)
    assert catalog.get(svc, "pims")["id"] == "pims"
    assert catalog.get(svc, "../pims") is None
