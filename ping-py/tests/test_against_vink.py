"""The client against a real vink server, deployed under /vink. Runs when
VINK_TEST_BINARY names a vink binary (make test and CI build one):

    VINK_TEST_BINARY=../bin/vink python3 -m unittest discover -s tests
"""

from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import vink_ping
from vink_ping import Client, Create, NotFound, StatusError

BINARY = os.environ.get("VINK_TEST_BINARY", "")


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


@unittest.skipUnless(BINARY, "set VINK_TEST_BINARY to a vink binary to run against a real server")
class TestAgainstVink(unittest.TestCase):
    server: subprocess.Popen[bytes]
    base: str
    api_key: str
    ping_key: str

    @classmethod
    def setUpClass(cls) -> None:
        cls.dir = tempfile.TemporaryDirectory()
        port = free_port()
        cls.base = f"http://127.0.0.1:{port}/vink"
        env = dict(
            os.environ,
            VINK_DB_PATH=os.path.join(cls.dir.name, "vink.db"),
            VINK_SECRETS_KEY_FILE=os.path.join(cls.dir.name, "secret.key"),
            VINK_SERVER_LISTEN=f"127.0.0.1:{port}",
            VINK_SERVER_BASE_URL=cls.base,
            VINK_CONFIG=os.path.join(cls.dir.name, "cli.toml"),
            VINK_LOG_LEVEL="warn",
            NO_COLOR="1",
        )
        init = subprocess.run(
            [BINARY, "admin", "init", "--org", "homelab", "--user", "py", "--password-stdin", "--timezone", "UTC", "--json"],
            input=b"python-test-password\n",
            env=env,
            capture_output=True,
            check=True,
        )
        keys = json.loads(init.stdout)
        cls.api_key, cls.ping_key = keys["api_key"], keys["ping_key"]
        cls.log = open(os.path.join(cls.dir.name, "serve.log"), "wb")  # noqa: SIM115 - closed in tearDownClass
        cls.server = subprocess.Popen([BINARY, "serve"], env=env, stdout=cls.log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 20
        while True:
            try:
                with urllib.request.urlopen(cls.base + "/healthz", timeout=1):
                    break
            except OSError:
                if time.monotonic() > deadline or cls.server.poll() is not None:
                    raise
                time.sleep(0.1)

    @classmethod
    def tearDownClass(cls) -> None:
        cls.server.terminate()
        cls.server.wait(timeout=10)
        cls.log.close()
        cls.dir.cleanup()

    def api(self, path: str) -> Any:
        req = urllib.request.Request(self.base + "/api/v1" + path, headers={"Authorization": "Bearer " + self.api_key})
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read())

    def observations(self, slug: str) -> list[dict[str, Any]]:
        items: list[dict[str, Any]] = self.api(f"/monitors/{slug}/observations?limit=50")["items"]
        return list(reversed(items))  # oldest first

    def client(self, **options: Any) -> Client:
        return Client(self.base + "/ping/", self.ping_key, user_agent="py-test", **options)

    def test_every_signal_is_recorded(self) -> None:
        m = self.client().monitor("py-signals")
        m.success(create=True, msg="created")
        m.start()
        m.log("step 2 of 5")
        m.log(body=b'{"files":3}', content_type="application/json")
        m.exit(0, body="all good\n")
        m.fail(msg="disk full")
        got = [
            (o["signal"], o["ok"], o.get("exit_code"), (o.get("detail") or {}).get("msg"), o["has_body"], o["user_agent"])
            for o in self.observations("py-signals")
        ]
        self.assertEqual(
            got,
            [
                ("ok", True, None, "created", False, "py-test"),
                ("start", False, None, None, False, "py-test"),
                ("log", False, None, "step 2 of 5", False, "py-test"),
                ("log", False, None, None, True, "py-test"),
                ("exit", True, 0, None, True, "py-test"),
                ("fail", False, None, "disk full", False, "py-test"),
            ],
        )
        self.assertEqual(self.api("/monitors/py-signals")["state"], "down")

    def test_a_run_is_paired_and_timed(self) -> None:
        m = self.client().monitor("py-run")
        m.success(create=True)
        with self.assertRaisesRegex(ValueError, "half way"), m.run() as run:
            run.log("started")
            time.sleep(0.3)
            raise ValueError("half way")
        obs = self.observations("py-run")[1:]
        self.assertEqual([o["signal"] for o in obs], ["start", "log", "fail"])
        self.assertEqual({o["run_id"] for o in obs}, {run.id})
        self.assertGreaterEqual(obs[2]["duration_ms"], 300)
        self.assertEqual(obs[2]["detail"]["msg"], "ValueError: half way")
        self.assertTrue(obs[2]["has_body"])

    def test_by_id_and_errors(self) -> None:
        c = self.client()
        c.monitor("py-id").success(create=True)
        monitor_id = self.api("/monitors/py-id")["id"]
        Client(self.base + "/ping/", "").monitor_id(monitor_id).fail()
        self.assertEqual(self.observations("py-id")[-1]["signal"], "fail")
        with self.assertRaises(NotFound):
            c.monitor("no-such-monitor").success()
        with self.assertRaises(NotFound):
            Client(self.base + "/ping/", "wrongkey").monitor("py-id").success()

    def test_413_from_a_monitor_limit(self) -> None:
        req = urllib.request.Request(
            self.base + "/api/v1/monitors",
            data=json.dumps({"name": "Small", "slug": "py-small", "schedule": {"period": "1h"}, "body_limit": 16}).encode(),
            headers={"Authorization": "Bearer " + self.api_key, "Content-Type": "application/json"},
            method="POST",
        )
        urllib.request.urlopen(req, timeout=5).close()
        self.client().monitor("py-small").fail(body="x" * 100 + "...the end of it")
        self.assertTrue(self.observations("py-small")[-1]["has_body"])

    def test_create_with_settings(self) -> None:
        m = self.client().monitor("py-create")
        made = m.success(create=Create(name="Py create", cron="0 3 * * *", tz="Europe/Amsterdam", grace="30m", tags=["py"]))
        self.assertIs(made, True)
        got = self.api("/monitors/py-create")
        self.assertEqual(
            (got["name"], got["schedule"]["cron"], got["timezone"], got["grace"], got["tags"]),
            ("Py create", "0 3 * * *", "Europe/Amsterdam", "30m", ["py"]),
        )
        # a later ping finds it and changes nothing
        self.assertIs(m.success(create=Create(grace="2h")), False)
        self.assertEqual(self.api("/monitors/py-create")["grace"], "30m")
        with self.assertRaises(StatusError) as cm:
            self.client().monitor("py-bad").success(create=Create(grace="5m", tolerance="10m"))
        self.assertEqual(cm.exception.status, 400)

    def test_version_is_sent(self) -> None:
        Client(self.base + "/ping/", self.ping_key).monitor("py-ua").success(create=True)
        self.assertEqual(self.observations("py-ua")[-1]["user_agent"], f"vink-ping-python/{vink_ping.__version__}")


if __name__ == "__main__":
    unittest.main()
