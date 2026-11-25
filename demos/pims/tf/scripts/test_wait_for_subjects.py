"""Run with: python3 -m unittest discover -s demos/pims/tf/scripts"""

import io
import os
import subprocess
import sys
import threading
import unittest
import urllib.error
from contextlib import redirect_stdout
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(__file__))
import wait_for_subjects as w  # noqa: E402

TOPICS = ["patients", "visits", "appointments"]


class Clock:
    def __init__(self):
        self.t = 0.0

    def now(self):
        return self.t

    def sleep(self, s):
        self.t += s


def run(fetch, timeout=100, poll=10):
    c = Clock()
    out = io.StringIO()
    with redirect_stdout(out):
        ok = w.wait("https://sr", "k", "s", TOPICS, timeout, poll, now=c.now, sleep=c.sleep, fetch=fetch)
    return ok, out.getvalue(), c.t


class WaitTests(unittest.TestCase):
    def test_returns_once_every_subject_exists(self):
        answers = iter([[], ["patients-value"], ["patients-value", "visits-value"], [f"{t}-value" for t in TOPICS] + ["other-value"]])
        ok, out, elapsed = run(lambda *a: next(answers))
        self.assertTrue(ok)
        self.assertEqual(elapsed, 30)
        self.assertIn("visits-value, appointments-value", out)

    def test_key_subjects_do_not_count(self):
        ok, _, _ = run(lambda *a: [f"{t}-key" for t in TOPICS], timeout=30)
        self.assertFalse(ok)

    def test_keeps_trying_through_errors_then_gives_up(self):
        def broken(*a):
            raise urllib.error.HTTPError("https://sr", 401, "unauthorised", {}, None)

        ok, out, elapsed = run(broken, timeout=30)
        self.assertFalse(ok)
        self.assertGreaterEqual(elapsed, 30)
        self.assertIn("(401)", out)

    def test_recovers_from_an_early_error(self):
        calls = []

        def fetch(*a):
            calls.append(1)
            if len(calls) == 1:
                raise urllib.error.URLError("not yet")
            return [f"{t}-value" for t in TOPICS]

        self.assertTrue(run(fetch)[0])


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        auth = self.headers.get("Authorization")
        if self.path != "/subjects" or auth != "Basic azpz":  # k:s
            self.send_response(401)
            self.end_headers()
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'["patients-value","visits-value","appointments-value"]')

    def log_message(self, *a):
        pass


class EndToEnd(unittest.TestCase):
    def test_the_script_against_a_server(self):
        srv = HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            self.assertEqual(w.subjects(f"http://127.0.0.1:{srv.server_port}", "k", "s"), ["patients-value", "visits-value", "appointments-value"])
            with self.assertRaises(urllib.error.HTTPError):
                w.subjects(f"http://127.0.0.1:{srv.server_port}", "k", "wrong")
        finally:
            srv.shutdown()

    def test_main_refuses_plain_http_and_missing_settings(self):
        script = os.path.join(os.path.dirname(__file__), "wait_for_subjects.py")
        base = {"PATH": os.environ["PATH"]}
        r = subprocess.run([sys.executable, script], env=base, capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)
        r = subprocess.run([sys.executable, script], capture_output=True, text=True,
                           env={**base, "SCHEMA_REGISTRY_URL": "http://insecure", "SCHEMA_REGISTRY_KEY": "k", "SCHEMA_REGISTRY_SECRET": "s", "TOPICS": "a"})
        self.assertEqual(r.returncode, 2)
        self.assertIn("https", r.stderr)


if __name__ == "__main__":
    unittest.main()
