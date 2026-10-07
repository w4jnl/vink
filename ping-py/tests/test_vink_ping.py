"""Unit tests against a scripted local HTTP server. Run from ping-py/:
python3 -m unittest discover -s tests
"""

from __future__ import annotations

import http.server
import re
import subprocess
import sys
import threading
import unittest
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import vink_ping
from vink_ping import Client, Create, NotFound, PingError, RateLimited, StatusError, Unreachable


class Fake:
    """A vink ping endpoint that records requests and answers from a script:
    one (status, headers) per request, 200 once the script runs out."""

    def __init__(self, *answers: tuple[int, dict[str, str]]):
        self.requests: list[dict[str, str]] = []
        self.answers = list(answers)
        fake = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n)
                u = urllib.parse.urlsplit(self.path)
                fake.requests.append(
                    {
                        "path": u.path,
                        "query": u.query,
                        "body": body.decode("utf-8", "replace"),
                        "raw": body,
                        "content_type": self.headers.get("Content-Type") or "",
                        "user_agent": self.headers.get("User-Agent") or "",
                    }
                )
                status, headers = fake.answers.pop(0) if fake.answers else (200, {})
                self.send_response(status)
                for k, v in headers.items():
                    self.send_header(k, v)
                self.send_header("Content-Length", "3")
                self.end_headers()
                self.wfile.write(b"OK\n")

            def log_message(self, *args: object) -> None:
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def client(self, key: str = "k", **options: object) -> tuple[Client, list[float]]:
        c = Client(self.url + "/vink/ping/", key, **options)  # type: ignore[arg-type]
        waits: list[float] = []
        c._sleep = waits.append
        return c, waits


class FakeTest(unittest.TestCase):
    def fake(self, *answers: tuple[int, dict[str, str]]) -> Fake:
        f = Fake(*answers)
        self.addCleanup(f.close)
        return f


class TestSignals(FakeTest):
    def test_each_signal_and_option(self) -> None:
        f = self.fake()
        c, _ = f.client("k3y")
        m = c.monitor("nightly-backup")
        cases = [
            (lambda: m.success(), "/vink/ping/k3y/nightly-backup", "", "", ""),
            (lambda: m.start(), "/vink/ping/k3y/nightly-backup/start", "", "", ""),
            (lambda: m.fail(msg="disk full"), "/vink/ping/k3y/nightly-backup/fail", "msg=disk+full", "", ""),
            (lambda: m.exit(3, body="out\n"), "/vink/ping/k3y/nightly-backup/3", "", "out\n", "text/plain; charset=utf-8"),
            (lambda: m.exit(0), "/vink/ping/k3y/nightly-backup/0", "", "", ""),
            (lambda: m.log("step 2 of 5"), "/vink/ping/k3y/nightly-backup/log", "msg=step+2+of+5", "", ""),
            (
                lambda: m.log(body=b'{"files":3}', content_type="application/json"),
                "/vink/ping/k3y/nightly-backup/log",
                "",
                '{"files":3}',
                "application/json",
            ),
            (lambda: m.success(run_id="r1", create=True), "/vink/ping/k3y/nightly-backup", "rid=r1&create=1", "", ""),
            (
                lambda: c.monitor_id("01J9Z3K6V4W8X2Y5Z7A9B1C3D5").fail(),
                "/vink/ping/id/01J9Z3K6V4W8X2Y5Z7A9B1C3D5/fail",
                "",
                "",
                "",
            ),
            (lambda: c.monitor("a b").success(), "/vink/ping/k3y/a%20b", "", "", ""),
        ]
        for send, path, query, body, ctype in cases:
            with self.subTest(path=path, query=query):
                send()
                r = f.requests[-1]
                self.assertEqual((r["path"], r["query"], r["body"], r["content_type"]), (path, query, body, ctype))
                self.assertEqual(r["user_agent"], f"vink-ping-python/{vink_ping.__version__}")
        self.assertEqual(len(f.requests), len(cases))

    def test_refused_before_sending(self) -> None:
        f = self.fake()
        c, _ = f.client()
        no_key, _ = f.client("")
        cases = [
            (lambda: c.monitor("job").log(), "needs a message or a body"),
            (lambda: c.monitor("job").new_run().log(""), "needs a message or a body"),
            (lambda: c.monitor("job").exit(-1), "outside 0"),
            (lambda: c.monitor("job").exit(True), "outside 0"),
            (lambda: c.monitor(""), "needs a slug"),
            (lambda: c.monitor_id(""), "needs an id"),
            (lambda: no_key.monitor("job"), "needs the project's ping key"),
            (lambda: c.monitor_id("x").success(create=True), "by slug"),
        ]
        for send, want in cases:
            with self.subTest(want=want), self.assertRaisesRegex(ValueError, want):
                send()
        self.assertEqual(f.requests, [])
        no_key.monitor_id("x").success()  # a client without a key pings by id
        self.assertEqual(f.requests[0]["path"], "/vink/ping/id/x")


class TestConstruction(unittest.TestCase):
    def test_client(self) -> None:
        good = {
            "https://vink.example.com/ping/": "https://vink.example.com/ping/",
            "https://vink.example.com/ping": "https://vink.example.com/ping/",
            "https://www.example.com/vink/ping//": "https://www.example.com/vink/ping/",
        }
        for url, base in good.items():
            self.assertEqual(Client(url, "k")._base, base)
        for url in [
            "https://vink.example.com",
            "https://vink.example.com/ping/k/job",
            "vink.example.com/ping/",
            "https://vink.example.com/ping/?a=1",
            "ftp://vink.example.com/ping/",
        ]:
            with self.subTest(url=url), self.assertRaisesRegex(ValueError, "ending in /ping/"):
                Client(url, "k")
        for key in ["https://vink.example.com/ping/k", "k y", "k?x"]:
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "the key alone"):
                Client("https://vink.example.com/ping/", key)

    def test_from_env(self) -> None:
        cases = [
            ({"VINK_PING_URL": "https://vink.example.com/ping/", "VINK_PING_KEY": "k"}, "https://vink.example.com/ping/"),
            ({"VINK_SERVER": "https://www.example.com/vink/", "VINK_PING_KEY": "k"}, "https://www.example.com/vink/ping/"),
            (
                {
                    "VINK_PING_URL": "https://ping.example.com/ping",
                    "VINK_SERVER": "https://vink.example.com",
                    "VINK_PING_KEY": "k",
                },
                "https://ping.example.com/ping/",
            ),
        ]
        for env, base in cases:
            c = Client.from_env(env, user_agent="job-host")
            self.assertEqual((c._base, c._key, c._user_agent), (base, "k", "job-host"))
        with self.assertRaisesRegex(ValueError, "VINK_PING_URL"):
            Client.from_env({"VINK_PING_KEY": "k"})
        with self.assertRaisesRegex(ValueError, "VINK_PING_KEY"):
            Client.from_env({"VINK_PING_URL": "https://vink.example.com/ping/"})


class TestRetries(FakeTest):
    def test_5xx_is_retried_until_it_succeeds(self) -> None:
        f = self.fake((503, {}), (503, {}))
        c, waits = f.client()
        c.monitor("job").success()
        self.assertEqual(len(f.requests), 3)
        self.assertEqual(len(waits), 2)
        self.assertGreaterEqual(waits[0], 0.5)
        self.assertGreaterEqual(waits[1], 1.0)

    def test_attempts_run_out(self) -> None:
        f = self.fake(*[(503, {})] * 4)
        c, _ = f.client()
        with self.assertRaises(StatusError) as cm:
            c.monitor("job").success()
        self.assertEqual(cm.exception.status, 503)
        self.assertEqual(len(f.requests), vink_ping.DEFAULT_ATTEMPTS)

    def test_one_attempt(self) -> None:
        f = self.fake((503, {}))
        c, _ = f.client(attempts=0)
        with self.assertRaises(StatusError):
            c.monitor("job").success()
        self.assertEqual(len(f.requests), 1)

    def test_404_is_final(self) -> None:
        f = self.fake((404, {}))
        c, _ = f.client()
        with self.assertRaises(NotFound):
            c.monitor("job").success()
        self.assertEqual(len(f.requests), 1)

    def test_429_waits_for_retry_after(self) -> None:
        f = self.fake((429, {"Retry-After": "4"}))
        c, waits = f.client()
        c.monitor("job").success()
        self.assertEqual(waits, [4.0])

    def test_429_with_a_long_retry_after_gives_up(self) -> None:
        f = self.fake((429, {"Retry-After": "120"}))
        c, _ = f.client()
        with self.assertRaises(RateLimited) as cm:
            c.monitor("job").success()
        self.assertEqual(cm.exception.retry_after, 120.0)
        self.assertEqual(len(f.requests), 1)

    def test_unreachable_is_retried_and_hides_the_key(self) -> None:
        f = self.fake()
        f.close()  # nothing listens any more
        c, waits = f.client("s3cretkey")
        with self.assertRaises(Unreachable) as cm:
            c.monitor("job").start(msg="x")
        self.assertEqual(len(waits), vink_ping.DEFAULT_ATTEMPTS - 1)
        text = str(cm.exception)
        self.assertNotIn("s3cretkey", text)
        self.assertIn("/vink/ping/<ping key>/job/start", text)
        self.assertNotIn("msg=", text)
        self.assertIsNotNone(cm.exception.__cause__)
        with self.assertRaises(Unreachable) as cm:
            c.monitor_id("01J9Z3K6V4W8X2Y5Z7A9B1C3D5").success()
        self.assertNotIn("01J9Z3K6V4W8X2Y5Z7A9B1C3D5", str(cm.exception))
        self.assertIn("id/<id>", str(cm.exception))


class TestLimits(FakeTest):
    def test_a_long_body_keeps_its_end_on_a_whole_character(self) -> None:
        # "abcdéfg" is 8 bytes, é two of them: a 3-byte tail starts inside é
        for limit, want in {3: "fg", 4: "éfg", 8: "abcdéfg"}.items():
            f = self.fake()
            c, _ = f.client(body_limit=limit)
            c.monitor("job").fail(body="abcdéfg")
            self.assertEqual(f.requests[0]["body"], want, limit)

    def test_a_binary_body_is_cut_by_bytes(self) -> None:
        f = self.fake()
        c, _ = f.client(body_limit=3)
        c.monitor("job").log(body=bytes([1, 2, 3, 0x80, 0x81]), content_type="application/octet-stream")
        self.assertEqual(f.requests[0]["raw"], bytes([3, 0x80, 0x81]))

    def test_413_is_sent_again_cut_to_the_monitors_limit(self) -> None:
        f = self.fake((413, {"Ping-Body-Limit": "4"}))
        c, _ = f.client()
        c.monitor("job").exit(1, body="line one\nlast")
        self.assertEqual([r["body"] for r in f.requests], ["line one\nlast", "last"])

    def test_a_second_413_is_final(self) -> None:
        f = self.fake((413, {"Ping-Body-Limit": "4"}), (413, {"Ping-Body-Limit": "4"}))
        c, _ = f.client()
        with self.assertRaises(StatusError) as cm:
            c.monitor("job").exit(1, body="0123456789")
        self.assertEqual((cm.exception.status, cm.exception.body_limit, len(f.requests)), (413, 4, 2))

    def test_a_long_message_is_cut_on_a_character(self) -> None:
        f = self.fake()
        c, _ = f.client()
        c.monitor("job").fail(msg="a" * (vink_ping.MAX_MSG_LEN - 1) + "é and more")
        sent = urllib.parse.parse_qs(f.requests[0]["query"])["msg"][0]
        self.assertEqual(sent, "a" * (vink_ping.MAX_MSG_LEN - 1))


class TestCreate(FakeTest):
    def test_settings_travel_and_the_answer_is_returned(self) -> None:
        from datetime import timedelta

        f = self.fake((200, {"Ping-Monitor": "created"}), (200, {"Ping-Monitor": "existing"}), (400, {}))
        c, waits = f.client()
        m = c.monitor("job")
        made = m.success(
            create=Create(
                name="Nightly backup",
                cron="0 3 * * *",
                tz="Europe/Amsterdam",
                grace="30m",
                tolerance=timedelta(minutes=1),
                max_runtime=timedelta(hours=2),
                tags=["backup", "prod"],
            )
        )
        self.assertIs(made, True)
        self.assertEqual(
            query(f.requests[0]),
            {
                "create": "1",
                "name": "Nightly backup",
                "cron": "0 3 * * *",
                "tz": "Europe/Amsterdam",
                "grace": "30m",
                "tolerance": "60s",
                "max_runtime": "7200s",
                "tags": "backup,prod",
            },
        )
        self.assertIs(m.start(create=Create(period="1h")), False)
        self.assertEqual(query(f.requests[1])["period"], "1h")
        with self.assertRaises(StatusError) as cm:
            m.success(create=Create(grace="1s"))
        self.assertEqual(cm.exception.status, 400)
        self.assertEqual((len(f.requests), waits), (3, []))
        # create=True is the plain create of before; without create, None
        self.assertIs(m.success(create=True), False)
        self.assertEqual(f.requests[3]["query"], "create=1")
        self.assertIsNone(m.success())

    def test_refused_before_sending(self) -> None:
        f = self.fake()
        c, _ = f.client()
        for create, want in [
            (Create(period="1h", cron="0 3 * * *"), "not both"),
            (Create(tags="backup"), "list of strings"),  # type: ignore[arg-type]
            (Create(grace=""), "timedelta or a string"),
            ("yes", "True or a Create"),
        ]:
            with self.assertRaises(ValueError) as cm:
                c.monitor("job").success(create=create)  # type: ignore[arg-type]
            self.assertIn(want, str(cm.exception))
        self.assertEqual(f.requests, [])


def signals(f: Fake) -> list[str]:
    return [re.sub(r"^/vink/ping/k/job", "", r["path"]) or "/" for r in f.requests]


def query(r: dict[str, str]) -> dict[str, str]:
    return {k: v[0] for k, v in urllib.parse.parse_qs(r["query"]).items()}


class TestRuns(FakeTest):
    def test_a_run_shares_its_id(self) -> None:
        f = self.fake()
        c, _ = f.client()
        run = c.monitor("job").new_run()
        self.assertEqual(len(run.id), 32)
        self.assertNotEqual(run.id, c.monitor("job").new_run().id)
        run.start()
        run.log("half way")
        run.finish()
        run.exit(0)
        run.fail()
        run.success()
        self.assertEqual(signals(f), ["/start", "/log", "/", "/0", "/fail", "/"])
        self.assertTrue(all(query(r)["rid"] == run.id for r in f.requests))
        self.assertEqual(query(f.requests[1])["msg"], "half way")
        self.assertEqual(c.monitor("job").new_run("mine").id, "mine")

    def test_finish_maps_the_outcome(self) -> None:
        def raised(exc: BaseException) -> BaseException:
            try:
                raise exc
            except BaseException as e:
                return e

        cases = [
            (raised(ValueError("bad input")), "/fail", "ValueError: bad input", "Traceback"),
            (raised(KeyError("x")), "/fail", "KeyError: 'x'", "Traceback"),
            (raised(RuntimeError()), "/fail", "RuntimeError", "Traceback"),
            (raised(SystemExit(0)), "/", None, ""),
            (raised(SystemExit(None)), "/", None, ""),
            (raised(SystemExit(4)), "/4", "SystemExit: 4", ""),
            (raised(SystemExit("bye")), "/1", "SystemExit: bye", ""),
            (
                raised(subprocess.CalledProcessError(2, ["tar"], output=b"out\n", stderr=b"err\n")),
                "/2",
                "CalledProcessError: Command '['tar']' returned non-zero exit status 2.",
                "out\nerr\n",
            ),
            (raised(subprocess.CalledProcessError(-9, ["tar"])), "/128", None, ""),
        ]
        for exc, path, msg, body in cases:
            with self.subTest(exc=repr(exc)):
                f = self.fake()
                c, _ = f.client()
                c.monitor("job").new_run().finish(exc)
                r = f.requests[0]
                self.assertEqual(signals(f), [path])
                if msg is not None:
                    self.assertEqual(query(r).get("msg"), msg)
                if body == "Traceback":
                    self.assertTrue(r["body"].startswith("Traceback (most recent call last):"), r["body"])
                else:
                    self.assertEqual(r["body"], body)

    def test_finish_with_msg_and_body_replaces_both(self) -> None:
        f = self.fake()
        c, _ = f.client()
        c.monitor("job").new_run().finish(ValueError("x"), msg="replaced", body="tail")
        self.assertEqual((query(f.requests[0])["msg"], f.requests[0]["body"]), ("replaced", "tail"))

    def test_with_block_success(self) -> None:
        f = self.fake()
        c, _ = f.client()
        with c.monitor("job").run() as run:
            run.log("inside")
        self.assertEqual(signals(f), ["/start", "/log", "/"])
        self.assertEqual(len({query(r)["rid"] for r in f.requests}), 1)

    def test_with_block_reports_and_reraises(self) -> None:
        f = self.fake()
        c, _ = f.client()
        with self.assertRaisesRegex(ValueError, "boom"), c.monitor("job").run():
            raise ValueError("boom")
        self.assertEqual(signals(f), ["/start", "/fail"])
        self.assertEqual(query(f.requests[1])["msg"], "ValueError: boom")
        self.assertIn('raise ValueError("boom")', f.requests[1]["body"])

    def test_keyboard_interrupt_is_reported(self) -> None:
        f = self.fake()
        c, _ = f.client()
        with self.assertRaises(KeyboardInterrupt), c.monitor("job").run():
            raise KeyboardInterrupt
        self.assertEqual(signals(f), ["/start", "/fail"])
        self.assertEqual(query(f.requests[1])["msg"], "KeyboardInterrupt")

    def test_ping_failures_never_change_the_outcome(self) -> None:
        f = self.fake()
        f.close()
        seen: list[PingError] = []
        c, _ = f.client(attempts=1, on_error=seen.append)
        with c.monitor("job").run():
            done = True
        self.assertTrue(done)
        self.assertEqual(len(seen), 2)
        with self.assertRaisesRegex(ValueError, "the job's own"), c.monitor("job").run():
            raise ValueError("the job's own")
        self.assertEqual(len(seen), 4)
        self.assertTrue(all(isinstance(e, Unreachable) for e in seen))
        quiet, _ = f.client(attempts=1)
        with quiet.monitor("job").run():
            pass  # no handler: dropped

    def test_job_decorator(self) -> None:
        f = self.fake()
        c, _ = f.client()

        @c.monitor("job").job
        def add(a: int, b: int) -> int:
            """Adds."""
            return a + b

        self.assertEqual(add(2, 3), 5)
        self.assertEqual(add.__doc__, "Adds.")
        self.assertEqual(signals(f), ["/start", "/"])

        @c.monitor("job").job
        def broken() -> None:
            raise OSError("disk")

        with self.assertRaises(OSError):
            broken()
        self.assertEqual(signals(f)[2:], ["/start", "/fail"])


class TestOptions(FakeTest):
    def test_user_agent(self) -> None:
        f = self.fake()
        c, _ = f.client(user_agent="nas-backup")
        c.monitor("job").success()
        self.assertEqual(f.requests[0]["user_agent"], "nas-backup")

    def test_version_matches_pyproject(self) -> None:
        text = (Path(__file__).resolve().parent.parent / "pyproject.toml").read_text()
        m = re.search(r'^version = "([^"]+)"', text, re.M)
        self.assertIsNotNone(m)
        assert m is not None
        self.assertEqual(m.group(1), vink_ping.__version__)


if __name__ == "__main__":
    unittest.main()
