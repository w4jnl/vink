"""Send heartbeat pings to a vink server from Python.

A job tells vink when it starts, how it ended and how far it got, and vink
alerts when a ping does not arrive or reports a failure. This is the Python
form of ``vink ping`` and ``vink run``. It needs the project's ping key,
never an API key, uses only the standard library, and works on Python 3.9
and later. vink is at https://github.com/w4jnl/vink; the ping protocol is in
its docs/heartbeats.md.

What you need:

- The ping URL up to the key: ``https://vink.example.com/ping/``, or
  ``https://www.example.com/vink/ping/`` when vink lives under a path. Every
  ping URL vink shows starts with it.
- The project's ping key: Settings > Keys in vink, or the part after
  ``/ping/`` in any of the project's ping URLs. Keep it out of source code
  and pass it in the environment.
- The monitor's slug: the last part of its ping URL. With ``create=True``
  the first ping makes the monitor; ``create=Create(cron="0 3 * * *",
  grace="30m")`` also sets it up (on create only).

Quick start::

    import vink_ping

    client = vink_ping.Client.from_env()  # VINK_PING_URL and VINK_PING_KEY
    with client.monitor("nightly-backup").run():
        backup()

The ``with`` block sends a start, then a success, or a failure carrying the
exception's text and traceback, and lets the exception go on. vink shows the
run's duration. A ping that cannot be sent never changes what the block
raises; it goes to ``on_error`` instead.

Signals, one ping each::

    m = client.monitor("nightly-backup")
    m.success()                       # ran and succeeded: up, next deadline from now
    m.start()                         # began: a run opens, nothing else changes
    m.fail(msg="disk full")           # failed: down, alerts carry the message
    m.exit(3, body=output)            # an exit code: 0 succeeds, anything else fails
    m.log("step 2 of 5 done")         # a note in the history; changes nothing

Every signal takes ``msg`` (a line shown on the observation and in alerts),
``body`` (stored detail such as a command's output, ``str`` or ``bytes``),
``content_type``, ``run_id`` and ``create``. With ``create``, a signal returns
whether this ping made the monitor (``True``) or found it there (``False``,
and the :class:`Create` settings were not used).

Errors: a ping that could not be recorded raises a :class:`PingError`:
:class:`NotFound` for a wrong key, slug or id, :class:`RateLimited`,
:class:`StatusError` for any other answer, :class:`Unreachable` when vink did
not answer. Messages never contain the ping key or a monitor id. Mistakes in
the arguments raise ``ValueError`` before anything is sent.
"""

from __future__ import annotations

import functools
import os
import random
import ssl
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
import uuid
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from datetime import timedelta
from types import TracebackType
from typing import Any, Callable, Optional, TypeVar, Union

__all__ = [
    "DEFAULT_ATTEMPTS",
    "DEFAULT_BODY_LIMIT",
    "DEFAULT_TIMEOUT",
    "MAX_MSG_LEN",
    "Client",
    "Create",
    "Monitor",
    "NotFound",
    "PingError",
    "RateLimited",
    "Run",
    "StatusError",
    "Unreachable",
]

__version__ = "0.2.1"

#: The body size a vink server keeps unless its operator changed
#: ``ping.body_limit``. A longer body is cut to its last bytes.
DEFAULT_BODY_LIMIT = 64 * 1024

#: The longest message the server keeps, in bytes. A longer ``msg`` is cut on
#: a character boundary before it is sent.
MAX_MSG_LEN = 2000

#: How many times a ping is tried when vink cannot be reached, answers 5xx or
#: rate-limits it.
DEFAULT_ATTEMPTS = 3

#: Seconds one attempt waits for the connection and for each read.
DEFAULT_TIMEOUT = 10.0

# the longest pause before a retry; a Retry-After above this ends the ping
_MAX_RETRY_WAIT = 30.0
_KEY_FORBIDDEN = set("/?#%& \t\r\n")

Body = Union[str, bytes, None]
Duration = Union[str, timedelta, None]
F = TypeVar("F", bound=Callable[..., object])


@dataclass(frozen=True)
class Create:
    """How the monitor a creating ping makes is set up. Pass it as
    ``create=``; ``create=True`` is the same as ``Create()``: a heartbeat
    with a one-day period and an hour of grace.

    Durations are a :class:`datetime.timedelta` or a vink string such as
    ``"90s"``, ``"30m"``, ``"2h"``, ``"1d"`` or ``"1d12h"``. Give ``period``
    or ``cron``, not both; ``tz`` is an IANA name and the project's when
    left out. The settings apply on create only: a ping never changes a
    monitor that exists. Settings vink refuses (a grace shorter than the
    tolerance, an unknown timezone) raise a :class:`StatusError` with
    status 400, and nothing is recorded."""

    name: Optional[str] = None
    period: Duration = None
    cron: Optional[str] = None
    tz: Optional[str] = None
    grace: Duration = None
    tolerance: Duration = None
    max_runtime: Duration = None
    tags: Sequence[str] = field(default_factory=tuple)

    def query(self) -> list[tuple[str, str]]:
        if self.period is not None and self.cron:
            raise ValueError("give period or cron, not both")
        if isinstance(self.tags, str):
            raise ValueError("tags is a list of strings, like ['backup', 'prod']")
        q: list[tuple[str, str]] = []
        for key, value in (("name", self.name), ("cron", self.cron), ("tz", self.tz)):
            if value:
                q.append((key, value))
        for key, d in (
            ("period", self.period),
            ("grace", self.grace),
            ("tolerance", self.tolerance),
            ("max_runtime", self.max_runtime),
        ):
            if d is not None:
                q.append((key, _duration(key, d)))
        if self.tags:
            q.append(("tags", ",".join(self.tags)))
        return q


def _duration(key: str, d: Union[str, timedelta]) -> str:
    if isinstance(d, timedelta):
        seconds = int(d.total_seconds())
        if seconds <= 0:
            raise ValueError(f"{key} must be at least a second")
        return f"{seconds}s"
    if not isinstance(d, str) or not d.strip():
        raise ValueError(f"{key} is a timedelta or a string like '30m'")
    return d.strip()


class PingError(Exception):
    """A ping that vink did not record. Safe to log: the message never
    contains the ping key or a monitor id."""


class StatusError(PingError):
    """vink answered with a status other than 200."""

    def __init__(
        self,
        status: int,
        reason: str,
        retry_after: Optional[float] = None,
        body_limit: Optional[int] = None,
        detail: str = "",
    ):
        message = f"vink answered {status} {reason}".rstrip()
        super().__init__(f"{message}: {detail}" if detail else message)
        #: The HTTP status, such as 404.
        self.status = status
        #: The reason phrase, such as "Not Found".
        self.reason = reason
        #: Seconds from Retry-After, on a 429.
        self.retry_after = retry_after
        #: The body size the monitor accepts, from Ping-Body-Limit.
        self.body_limit = body_limit
        #: vink's reason on a 400, such as why it refused the settings of a
        #: :class:`Create`: "create: grace: must be at least 60s".
        self.detail = detail


class NotFound(StatusError):
    """404: vink knows no monitor for this key and slug, or for this id. A
    slug ping with ``create=True`` makes the monitor instead."""


class RateLimited(StatusError):
    """429: the monitor or this address sent too many pings, and vink
    dropped this one."""


class Unreachable(PingError):
    """vink did not answer: no connection, a timeout, a TLS failure. The
    original exception is the ``__cause__``."""


def _base_url(raw: str) -> str:
    u = urllib.parse.urlsplit(raw)
    if u.scheme not in ("http", "https") or not u.netloc or u.query or u.fragment or not u.path.rstrip("/").endswith("/ping"):
        raise ValueError(
            f"the ping URL is a ping URL up to the key, ending in /ping/, like https://vink.example.com/ping/; got {raw!r}"
        )
    return urllib.parse.urlunsplit((u.scheme, u.netloc, u.path.rstrip("/") + "/", "", ""))


def _cut_msg(msg: str) -> str:
    raw = msg.encode("utf-8")
    if len(raw) <= MAX_MSG_LEN:
        return msg
    return raw[:MAX_MSG_LEN].decode("utf-8", errors="ignore")


def _keep_tail(body: bytes, limit: int, content_type: str) -> bytes:
    if limit <= 0 or len(body) <= limit:
        return body
    body = body[-limit:]
    if content_type.startswith("text/"):
        # start on a whole character: drop continuation bytes, at most three
        for _ in range(3):
            if body and (body[0] & 0xC0) == 0x80:
                body = body[1:]
    return body


def _backoff(attempt: int) -> float:
    d = min(0.5 * 2.0 ** (attempt - 1), 5.0)
    return d + random.uniform(0, d / 4)


class Client:
    """Sends pings for one project to one vink server.

    Make it once and share it: it is safe to use from several threads.

    :param ping_url: a ping URL cut before the key, such as
        ``https://vink.example.com/ping/``.
    :param key: the project's ping key; may be empty when the client only
        pings by id (:meth:`monitor_id`).
    :param user_agent: names the sender; vink shows it with every
        observation, so ``"nas-backup"`` tells hosts apart.
    :param attempts: tries per ping, 1 to turn retries off.
    :param timeout: seconds one attempt waits for the connection and each read.
    :param body_limit: bodies are cut to their last ``body_limit`` bytes; 0
        sends them whole.
    :param on_error: receives ping failures that :meth:`Monitor.run` and
        :meth:`Monitor.job` swallow, for logging. The default drops them.
    :param ssl_context: for a private CA, for example
        ``ssl.create_default_context(cafile="/etc/ssl/corp-ca.pem")``.
    """

    def __init__(
        self,
        ping_url: str,
        key: str = "",
        *,
        user_agent: Optional[str] = None,
        attempts: int = DEFAULT_ATTEMPTS,
        timeout: float = DEFAULT_TIMEOUT,
        body_limit: int = DEFAULT_BODY_LIMIT,
        on_error: Optional[Callable[[PingError], object]] = None,
        ssl_context: Optional[ssl.SSLContext] = None,
    ):
        self._base = _base_url(ping_url)
        if _KEY_FORBIDDEN & set(key):
            raise ValueError("the ping key is the key alone, the part after /ping/ in a ping URL")
        self._key = key
        self._user_agent = user_agent or f"vink-ping-python/{__version__}"
        self._attempts = max(int(attempts), 1)
        self._timeout = timeout
        self._body_limit = max(int(body_limit), 0)
        self._on_error = on_error
        handlers: list[urllib.request.BaseHandler] = []
        if ssl_context is not None:
            handlers.append(urllib.request.HTTPSHandler(context=ssl_context))
        self._opener = urllib.request.build_opener(*handlers)
        self._sleep: Callable[[float], None] = time.sleep  # tests replace it

    @classmethod
    def from_env(cls, environ: Optional[Mapping[str, str]] = None, **options: Any) -> Client:
        """Makes a client from the environment the vink CLI reads:
        ``VINK_PING_URL`` (a ping URL up to the key) and ``VINK_PING_KEY``.
        Without ``VINK_PING_URL`` it uses ``VINK_SERVER`` with ``/ping/``
        added. Both an address and a key are required, so a missing variable
        shows at start-up rather than at the first ping. ``options`` are the
        keyword arguments of :class:`Client`.
        """
        env = os.environ if environ is None else environ
        url = env.get("VINK_PING_URL", "")
        if not url and env.get("VINK_SERVER"):
            url = env["VINK_SERVER"].rstrip("/") + "/ping/"
        if not url:
            raise ValueError("set VINK_PING_URL to a ping URL up to the key (https://vink.example.com/ping/), or VINK_SERVER")
        key = env.get("VINK_PING_KEY", "")
        if not key:
            raise ValueError("set VINK_PING_KEY to the project's ping key")
        return cls(url, key, **options)

    def monitor(self, slug: str) -> Monitor:
        """A monitor of the client's project, by its slug: the last part of
        its ping URL."""
        if not slug:
            raise ValueError("a monitor needs a slug")
        if not self._key:
            raise ValueError("a ping by slug needs the project's ping key")
        return Monitor(self, self._key + "/" + urllib.parse.quote(slug, safe=""), self._key, "<ping key>", by_slug=True)

    def monitor_id(self, monitor_id: str) -> Monitor:
        """A monitor by its id, which works without the project's key: the id
        alone lets its holder ping that one monitor, so treat it like a key.
        ``create`` does not apply."""
        if not monitor_id:
            raise ValueError("a monitor needs an id")
        escaped = urllib.parse.quote(monitor_id, safe="")
        return Monitor(self, "id/" + escaped, escaped, "<id>", by_slug=False)

    def _send(self, url: str, secret: str, shown: str, body: bytes, content_type: str) -> str:
        """Sends one ping with retries; returns the Ping-Monitor answer."""

        def hide(text: str) -> str:
            return text.replace(secret, shown) if secret else text

        shrunk = False
        attempt = 0
        while True:
            attempt += 1
            try:
                return self._once(url, body, content_type)
            except StatusError as e:
                err: PingError = e
                if e.status == 413 and not shrunk and e.body_limit and e.body_limit < len(body):
                    body, shrunk = _keep_tail(body, e.body_limit, content_type), True
                    attempt -= 1
                    continue
                if e.status == 429:
                    wait = e.retry_after or 0.0
                elif e.status >= 500:
                    wait = 0.0
                else:
                    raise
            except (urllib.error.URLError, OSError) as e:
                err = Unreachable(f"{hide(url.split('?')[0])}: {_reason(e)}")
                err.__cause__ = e
                wait = 0.0
            if attempt >= self._attempts:
                raise err
            if wait <= 0:
                wait = _backoff(attempt)
            if wait > _MAX_RETRY_WAIT:
                raise err
            self._sleep(wait)

    def _once(self, url: str, body: bytes, content_type: str) -> str:
        # no data at all for an empty body: urllib labels any data, even
        # b"", as a form post
        req = urllib.request.Request(url, data=body or None, method="POST")
        req.add_header("User-Agent", self._user_agent)
        if body:
            req.add_header("Content-Type", content_type)
        try:
            with self._opener.open(req, timeout=self._timeout) as resp:
                resp.read(4096)
                status = resp.status
                if status == 200:
                    return resp.headers.get("Ping-Monitor") or ""
                raise StatusError(status, resp.reason or "")
        except urllib.error.HTTPError as e:
            with e:
                retry_after = _number(e.headers.get("Retry-After"))
                body_limit = _number(e.headers.get("Ping-Body-Limit"))
                detail = _detail(e.read(4096)) if e.code == 400 else ""
                args = (e.code, e.reason or "", retry_after, int(body_limit) if body_limit else None, detail)
            if e.code == 404:
                raise NotFound(*args) from None
            if e.code == 429:
                raise RateLimited(*args) from None
            raise StatusError(*args) from None


def _detail(raw: bytes) -> str:
    """A plain-text answer as one line: its lines joined by "; ", at most
    500 characters."""
    text = raw.decode("utf-8", "replace")
    return "; ".join(line.strip() for line in text.splitlines() if line.strip())[:500]


def _number(v: Optional[str]) -> Optional[float]:
    try:
        n = float(v) if v is not None else None
    except ValueError:
        return None
    return n if n is not None and n > 0 else None


def _reason(e: BaseException) -> str:
    if isinstance(e, urllib.error.URLError):
        return str(e.reason)
    return str(e) or type(e).__name__


class Monitor:
    """One heartbeat monitor, by slug or by id. Cheap to make, immutable,
    safe to share. Make it with :meth:`Client.monitor` or
    :meth:`Client.monitor_id`."""

    def __init__(self, client: Client, path: str, secret: str, shown: str, *, by_slug: bool):
        self._client = client
        self._path = path
        self._secret = secret
        self._shown = shown
        self._by_slug = by_slug

    def success(
        self,
        *,
        msg: Optional[str] = None,
        body: Body = None,
        content_type: Optional[str] = None,
        run_id: Optional[str] = None,
        create: Union[bool, Create] = False,
    ) -> Optional[bool]:
        """The job ran and succeeded: the monitor goes up and the next
        deadline counts from now."""
        return self._send("", msg, body, content_type, run_id, create)

    def start(
        self,
        *,
        msg: Optional[str] = None,
        body: Body = None,
        content_type: Optional[str] = None,
        run_id: Optional[str] = None,
        create: Union[bool, Create] = False,
    ) -> Optional[bool]:
        """The job began. vink shows it running, measures its duration when
        the finish arrives, and fails it when the monitor's max_runtime
        passes first. It changes no state."""
        return self._send("start", msg, body, content_type, run_id, create)

    def fail(
        self,
        *,
        msg: Optional[str] = None,
        body: Body = None,
        content_type: Optional[str] = None,
        run_id: Optional[str] = None,
        create: Union[bool, Create] = False,
    ) -> Optional[bool]:
        """The job failed: the monitor goes down once its failure threshold
        is reached (one failure by default) and alerts go out."""
        return self._send("fail", msg, body, content_type, run_id, create)

    def exit(
        self,
        code: int,
        *,
        msg: Optional[str] = None,
        body: Body = None,
        content_type: Optional[str] = None,
        run_id: Optional[str] = None,
        create: Union[bool, Create] = False,
    ) -> Optional[bool]:
        """A process's exit code: 0 is a success, anything else a failure
        that carries the code into the alert."""
        if isinstance(code, bool) or not isinstance(code, int) or not 0 <= code <= 2**31 - 1:
            raise ValueError(f"exit code {code!r} is outside 0 to {2**31 - 1}")
        return self._send(str(code), msg, body, content_type, run_id, create)

    def log(
        self,
        msg: Optional[str] = None,
        *,
        body: Body = None,
        content_type: Optional[str] = None,
        run_id: Optional[str] = None,
        create: Union[bool, Create] = False,
    ) -> Optional[bool]:
        """A progress note in the monitor's history. It changes nothing
        else: no state, no deadline, and an open run stays open. ``msg`` is
        the note's line; ``body`` can carry more, alone or with it."""
        if not msg and not body:
            raise ValueError("a log ping needs a message or a body")
        return self._send("log", msg, body, content_type, run_id, create)

    def new_run(self, run_id: Optional[str] = None) -> Run:
        """Prepares a run: pings that share one run id, so vink pairs the
        start with its finish even when runs overlap. Nothing is sent yet."""
        return Run(self, run_id or uuid.uuid4().hex)

    def run(self) -> _RunContext:
        """Wraps a block as one run::

            with monitor.run() as run:
                step_one()
                run.log("step one done")
                step_two()

        On entry it sends a start. On exit it sends a success, or for an
        exception a failure with the exception's text as the message and its
        traceback as the body (an exit code for ``SystemExit`` and
        ``subprocess.CalledProcessError``). The exception is not swallowed.
        A ping that cannot be sent never changes the block's outcome; it goes
        to the client's ``on_error``.
        """
        return _RunContext(self)

    def job(self, fn: F) -> F:
        """A decorator that runs every call of ``fn`` inside :meth:`run`::

        @client.monitor("report").job
        def build_report(day): ...
        """

        @functools.wraps(fn)
        def wrapper(*args: object, **kwargs: object) -> object:
            with self.run():
                return fn(*args, **kwargs)

        return wrapper  # type: ignore[return-value]

    def _send(
        self,
        signal: str,
        msg: Optional[str],
        body: Body,
        content_type: Optional[str],
        run_id: Optional[str],
        create: Union[bool, Create],
    ) -> Optional[bool]:
        if create is True:
            create = Create()
        elif create is False:
            create = None  # type: ignore[assignment]
        elif not isinstance(create, Create):
            raise ValueError("create is True or a Create(...)")
        if create and not self._by_slug:
            raise ValueError("create works on pings by slug, not by id")
        c = self._client
        url = c._base + self._path + ("/" + signal if signal else "")
        query = []
        if msg:
            query.append(("msg", _cut_msg(msg)))
        if run_id:
            query.append(("rid", run_id))
        if create:
            query.append(("create", "1"))
            query.extend(create.query())
        if query:
            url += "?" + urllib.parse.urlencode(query)
        raw = body.encode("utf-8") if isinstance(body, str) else (body or b"")
        ctype = content_type or ("text/plain; charset=utf-8" if raw else "")
        state = c._send(url, self._secret, self._shown, _keep_tail(raw, c._body_limit, ctype), ctype)
        return state == "created" if create else None


class Run:
    """One run of a job: a start, any notes, and a finish, all carrying the
    same run id. Make one with :meth:`Monitor.new_run` per execution."""

    def __init__(self, monitor: Monitor, run_id: str):
        self._monitor = monitor
        #: The run id every ping of this run carries.
        self.id = run_id

    def start(self, *, msg: Optional[str] = None, body: Body = None, content_type: Optional[str] = None) -> None:
        """Sends the run's start. See :meth:`Monitor.start`."""
        self._monitor.start(msg=msg, body=body, content_type=content_type, run_id=self.id)

    def log(self, msg: Optional[str] = None, *, body: Body = None, content_type: Optional[str] = None) -> None:
        """Sends a note within the run. See :meth:`Monitor.log`."""
        self._monitor.log(msg, body=body, content_type=content_type, run_id=self.id)

    def success(self, *, msg: Optional[str] = None, body: Body = None, content_type: Optional[str] = None) -> None:
        """Ends the run as a success."""
        self._monitor.success(msg=msg, body=body, content_type=content_type, run_id=self.id)

    def fail(self, *, msg: Optional[str] = None, body: Body = None, content_type: Optional[str] = None) -> None:
        """Ends the run as a failure."""
        self._monitor.fail(msg=msg, body=body, content_type=content_type, run_id=self.id)

    def exit(self, code: int, *, msg: Optional[str] = None, body: Body = None, content_type: Optional[str] = None) -> None:
        """Ends the run with a process's exit code."""
        self._monitor.exit(code, msg=msg, body=body, content_type=content_type, run_id=self.id)

    def finish(
        self,
        exc: Optional[BaseException] = None,
        *,
        msg: Optional[str] = None,
        body: Body = None,
        content_type: Optional[str] = None,
    ) -> None:
        """Ends the run by its outcome. ``None`` is a success. An exception
        is a failure whose message is the exception's text and whose body is
        its traceback; ``msg`` and ``body`` replace either. ``SystemExit``
        and ``subprocess.CalledProcessError`` report their exit code instead,
        the latter with the output it captured as the body, and
        ``SystemExit(0)`` is a success."""
        if exc is None:
            self.success(msg=msg, body=body, content_type=content_type)
            return
        code = _exit_code(exc)
        if code == 0:
            self.success(msg=msg, body=body, content_type=content_type)
            return
        text = str(exc)
        if msg is None:
            msg = f"{type(exc).__name__}: {text}" if text else type(exc).__name__
        if body is None and code is None:
            body = "".join(traceback.format_exception(type(exc), exc, exc.__traceback__))
            content_type = content_type or "text/plain; charset=utf-8"
        elif body is None:
            body = _captured(exc)
        if code is not None:
            self.exit(code, msg=msg, body=body, content_type=content_type)
        else:
            self.fail(msg=msg, body=body, content_type=content_type)


def _exit_code(exc: BaseException) -> Optional[int]:
    """The exit code an exception stands for, or None for a plain failure."""
    if isinstance(exc, SystemExit):
        if exc.code is None:
            return 0
        if isinstance(exc.code, int) and not isinstance(exc.code, bool):
            return min(max(exc.code, 0), 2**31 - 1) if exc.code >= 0 else 1
        return 1  # SystemExit("message") exits with 1
    returncode = getattr(exc, "returncode", None)  # subprocess.CalledProcessError
    if isinstance(returncode, int) and not isinstance(returncode, bool):
        return returncode if returncode > 0 else 128
    return None


def _captured(exc: BaseException) -> Optional[bytes]:
    """What a CalledProcessError captured, stdout then stderr, or None."""
    out = b""
    for part in (getattr(exc, "stdout", None), getattr(exc, "stderr", None)):
        if isinstance(part, str):
            out += part.encode("utf-8", errors="replace")
        elif isinstance(part, bytes):
            out += part
    return out or None


class _RunContext:
    def __init__(self, monitor: Monitor):
        self._monitor = monitor
        self._run: Optional[Run] = None

    def __enter__(self) -> Run:
        self._run = self._monitor.new_run()
        self._report(self._run.start)
        return self._run

    def __exit__(
        self, exc_type: Optional[type[BaseException]], exc: Optional[BaseException], tb: Optional[TracebackType]
    ) -> None:
        # returns None, so the exception, if any, goes on
        run = self._run
        assert run is not None
        self._report(lambda: run.finish(exc))

    def _report(self, send: Callable[[], None]) -> None:
        # monitoring never changes the job's outcome: a ping that fails goes
        # to on_error, and nothing it raises replaces the job's exception
        try:
            send()
        except PingError as e:
            handler = self._monitor._client._on_error
            if handler is not None:
                handler(e)
        except Exception as e:  # a bug here must not mask the job's own error
            handler = self._monitor._client._on_error
            if handler is not None:
                handler(PingError(f"could not send the ping: {e!r}"))
