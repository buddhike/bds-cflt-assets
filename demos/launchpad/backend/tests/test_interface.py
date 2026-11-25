from launchpad import interface

from fakes import CC_TF, GOOD_TF


def test_good_demo():
    r = interface.check({"main.tf": GOOD_TF})
    assert r.valid and not r.needs_confluent


def test_confluent_demo_declares_both_variables():
    r = interface.check({"main.tf": CC_TF})
    assert r.valid and r.needs_confluent


def test_problems_are_all_reported():
    r = interface.check({"main.tf": 'terraform { backend "s3" {} }\nvariable "confluent_cloud_api_key" {}'})
    assert not r.valid
    assert 'missing variable "aws_region"' in r.problems
    assert 'missing variable "launchpad_tags"' in r.problems
    assert 'missing output "launchpad_url"' in r.problems
    assert any("backend" in p for p in r.problems)
    assert any("both confluent_cloud_api_key" in p for p in r.problems)


def test_commented_out_declarations_do_not_count():
    tf = '# variable "aws_region" {}\n// variable "launchpad_tags" {}\n/* output "launchpad_url" {} */'
    assert len(interface.check({"main.tf": tf}).problems) == 3


def test_no_files():
    assert interface.check({}).problems == ["tf has no .tf files"]


def test_declarations_spread_over_files():
    r = interface.check({"variables.tf": 'variable "aws_region" {}\nvariable "launchpad_tags" {}', "outputs.tf": 'output "launchpad_url" {}'})
    assert r.valid
