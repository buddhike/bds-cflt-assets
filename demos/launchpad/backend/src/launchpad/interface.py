"""The interface a demo's Terraform must implement. See docs/specs/002_design.md."""

import re
from dataclasses import dataclass, field

REQUIRED_VARIABLES = ("aws_region", "launchpad_tags")
REQUIRED_OUTPUT = "launchpad_url"
CONFLUENT_VARIABLES = ("confluent_cloud_api_key", "confluent_cloud_api_secret")

_VARIABLE = re.compile(r'^\s*variable\s+"([^"]+)"', re.M)
_OUTPUT = re.compile(r'^\s*output\s+"([^"]+)"', re.M)
_BACKEND = re.compile(r'\bbackend\s+"[^"]+"\s*\{')
_BLOCK_COMMENT = re.compile(r"/\*.*?\*/", re.S)
_LINE_COMMENT = re.compile(r"(?m)(#|//).*$")


@dataclass
class Interface:
    problems: list[str] = field(default_factory=list)
    needs_confluent: bool = False

    @property
    def valid(self) -> bool:
        return not self.problems


def _code(text: str) -> str:
    return _LINE_COMMENT.sub("", _BLOCK_COMMENT.sub("", text))


def check(tf_files: dict[str, str]) -> Interface:
    """Check the top-level .tf files of a demo's tf directory."""
    code = "\n".join(_code(t) for t in tf_files.values())
    variables = set(_VARIABLE.findall(code))
    outputs = set(_OUTPUT.findall(code))
    result = Interface()
    if not tf_files:
        result.problems.append("tf has no .tf files")
        return result
    for name in REQUIRED_VARIABLES:
        if name not in variables:
            result.problems.append(f'missing variable "{name}"')
    if REQUIRED_OUTPUT not in outputs:
        result.problems.append(f'missing output "{REQUIRED_OUTPUT}"')
    if _BACKEND.search(code):
        result.problems.append("declares a backend, but Launchpad supplies the S3 backend")
    declared = [v in variables for v in CONFLUENT_VARIABLES]
    if any(declared) and not all(declared):
        result.problems.append("declare both confluent_cloud_api_key and confluent_cloud_api_secret, or neither")
    result.needs_confluent = all(declared)
    return result
