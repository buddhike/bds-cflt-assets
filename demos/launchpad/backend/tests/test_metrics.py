import json

from launchpad import metrics


def test_emit_writes_one_emf_line(capsys):
    metrics.emit("DemoLaunchRequested", demo="pims")
    rec = json.loads(capsys.readouterr().out)
    cw = rec["_aws"]["CloudWatchMetrics"][0]
    assert cw["Namespace"] == "Launchpad"
    assert cw["Dimensions"] == [["demo"]]
    assert cw["Metrics"] == [{"Name": "DemoLaunchRequested", "Unit": "Count"}]
    assert rec["DemoLaunchRequested"] == 1 and rec["demo"] == "pims"


def test_emit_without_dimensions(capsys):
    metrics.emit("SignInStarted")
    rec = json.loads(capsys.readouterr().out)
    assert rec["_aws"]["CloudWatchMetrics"][0]["Dimensions"] == [[]]
