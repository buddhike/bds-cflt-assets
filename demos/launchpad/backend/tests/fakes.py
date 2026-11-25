"""A fake GitHub for tests: a table of URL to response bytes."""

import io
import json
import tarfile
import urllib.error

SHA = "a" * 40

GOOD_TF = '''
variable "aws_region" { type = string }
variable "launchpad_tags" { type = map(string) }
output "launchpad_url" { value = "http://example" }
'''
CC_TF = GOOD_TF + '''
variable "confluent_cloud_api_key" {
  type = string
}
variable "confluent_cloud_api_secret" {
  type      = string
  sensitive = true
}
'''


class Resp(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *a):
        self.close()


class FakeGitHub:
    def __init__(self):
        self.routes: dict[str, bytes] = {}
        self.calls: list[str] = []

    def add_repo(self, files: dict[str, str], repo="owner/repo", sha=SHA):
        """files maps repository paths to contents."""
        self.routes[f"https://api.github.com/repos/{repo}/commits/main"] = sha.encode()
        entries = []
        dirs = set()
        for path in files:
            parts = path.split("/")
            for i in range(1, len(parts)):
                dirs.add("/".join(parts[:i]))
            entries.append({"path": path, "type": "blob"})
            self.routes[f"https://raw.githubusercontent.com/{repo}/{sha}/{path}"] = files[path].encode()
        entries += [{"path": d, "type": "tree"} for d in dirs]
        self.routes[f"https://api.github.com/repos/{repo}/git/trees/{sha}?recursive=1"] = json.dumps(
            {"tree": entries, "truncated": False}
        ).encode()

    def __call__(self, req, timeout=None):
        url = req.full_url
        self.calls.append(url)
        if url not in self.routes:
            raise urllib.error.HTTPError(url, 404, "not found", {}, None)
        return Resp(self.routes[url])


def tar_gz(members: list[tuple[str, bytes | None, str]]) -> bytes:
    """members: (name, data, kind) with kind 'file', 'dir' or 'symlink' (data is the link target)."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for name, data, kind in members:
            info = tarfile.TarInfo(name)
            if kind == "dir":
                info.type = tarfile.DIRTYPE
                tar.addfile(info)
            elif kind == "symlink":
                info.type = tarfile.SYMTYPE
                info.linkname = data.decode()
                tar.addfile(info)
            else:
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()
