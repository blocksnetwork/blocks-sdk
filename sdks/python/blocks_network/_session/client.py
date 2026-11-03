"""The consumer client for sessions: open a session with an agent, or make one call."""

from __future__ import annotations

import logging
import threading
import time
from concurrent.futures import Future
from dataclasses import dataclass
from typing import Any, Callable, Dict, List, Optional, Set, Tuple, cast

from .._consumer_endpoints import resolve_consumer_endpoints
from ..pubnub_client import create_pubnub_client
from . import wire
from .errors import SessionError
from .futures import spawn
from .http import BEACON_CLOSED, OpenedSession, SessionHttp
from .scheduler import Scheduler, ThreadingScheduler
from .session import (
    SESSION_READY_TIMEOUT_S,
    Exchange,
    ExchangeOutcome,
    ReadyDeadline,
    Session,
    SessionInput,
    SessionLifecycle,
    SessionTransport,
    ready_deadline,
    ready_timeout_error,
    to_request_parts,
)

logger = logging.getLogger(__name__)

_CREATE = "BlocksClient.create()"
ALREADY_OPEN = "session_already_open"
MAX_IDEMPOTENCY_KEY_LENGTH = 256


def _destroyed_error() -> SessionError:
    return SessionError("session_canceled", "The BlocksClient was destroyed")


def _default_timing() -> Tuple[Scheduler, Callable[[], float]]:
    """Delayed callbacks (gap recovery, token rotation) and the clock they run against."""
    return ThreadingScheduler(), time.monotonic


def _check_idempotency_key(key: Any) -> None:
    if key is not None and (not isinstance(key, str) or len(key) > MAX_IDEMPOTENCY_KEY_LENGTH):
        raise ValueError(
            f"idempotency_key must be a string of at most {MAX_IDEMPOTENCY_KEY_LENGTH} characters"
        )


@dataclass
class _KeyHold:
    """How an open under an idempotency key reports the session it holds the key for."""

    free: Callable[[], None]
    created: bool = False
    # The open request was abandoned; this settles once its late answer has been handled.
    abandoned: Optional["Future[None]"] = None


@dataclass(frozen=True)
class _ClientConfig:
    subscribe_key: str
    publish_key: str
    http: SessionHttp
    owned_auth: Any = None


class BlocksClient:
    """The consumer client for sessions: open a session with an agent, or make one call.

    Build one with :meth:`create`.

    .. warning:: Experimental: sessions are a preview, refused with ``SESSIONS_DISABLED``
       until enabled for your org, and may change before general availability.
    """

    def __init__(self, config: _ClientConfig) -> None:
        self._config = config
        self._lock = threading.Lock()
        self._sessions: Set[Session] = set()
        self._destroyed = False
        # Open requests still in flight, abandoned by destroy().
        self._pending_opens: Set[threading.Event] = set()
        # Keys whose session this client opened and has not released. A key names one open,
        # and until the provider's checkpoint lands the backend may still report its session
        # open, so a second open under it would join the same session as another holder.
        self._keys_held: Set[str] = set()

    @classmethod
    def create(
        cls,
        billing_mode: str,
        *,
        cdm_url: Optional[str] = None,
        subscribe_key: Optional[str] = None,
        publish_key: Optional[str] = None,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
        token_endpoint: Any = None,
        token_provider: Optional[Callable[[], Any]] = None,
        auth_provider: Any = None,
        on_auth_error: Optional[Callable[[Exception], None]] = None,
        rpc_headers: Optional[Dict[str, str]] = None,
    ) -> "BlocksClient":
        """Resolve endpoints as :meth:`TaskClient.create` does, from exactly one credential:
        ``api_key``, ``token_endpoint``, ``token_provider`` or ``auth_provider``.

        ``rpc_headers`` are sent on every session call, under the SDK's own headers.
        """
        credentials = sum(
            1 for c in (api_key, token_endpoint, token_provider, auth_provider) if c
        )
        if credentials != 1:
            raise ValueError(
                f"{_CREATE} requires exactly one of api_key, token_endpoint, "
                "token_provider or auth_provider"
            )
        endpoints = resolve_consumer_endpoints(
            billing_mode,
            cdm_url=cdm_url,
            subscribe_key=subscribe_key,
            publish_key=publish_key,
            base_url=base_url,
        )
        if not endpoints.publish_key:
            raise ValueError(f"{_CREATE} could not resolve publish_key from options, env, or CDM")
        owned_auth = None
        if auth_provider is None:
            from ..consumer_auth import ConsumerAuth

            owned_auth = ConsumerAuth(
                api_key=api_key,
                token_endpoint=token_endpoint,
                token_provider=token_provider,
                base_url=endpoints.base_url,
                on_auth_error=on_auth_error,
            )
            owned_auth.init()
        http = SessionHttp(endpoints.base_url, auth_provider or owned_auth, rpc_headers)
        return cls(_ClientConfig(endpoints.subscribe_key, endpoints.publish_key, http, owned_auth))

    def open_session(
        self,
        agent_name: str,
        *,
        idempotency_key: Optional[str] = None,
        ready_timeout: float = SESSION_READY_TIMEOUT_S,
    ) -> Session:
        """Return once the session is ready, or raise the refusal or terminal error.

        ``ready_timeout`` bounds the wait, in seconds, from the open request to the agent
        reporting ready: greater than 0 and at most 120. ``idempotency_key`` (at most 256
        characters) makes the open safe to retry from this client, which refuses a second
        open under a key with ``session_already_open`` until the first has torn down. Reuse
        a key from another client only once the earlier open's process is gone.
        """
        session, _ = self._open(agent_name, idempotency_key, ready_timeout)
        return session

    def call(
        self,
        agent_name: str,
        request: SessionInput,
        *,
        timeout: Optional[float] = None,
        idempotency_key: Optional[str] = None,
        ready_timeout: float = SESSION_READY_TIMEOUT_S,
    ) -> ExchangeOutcome:
        """Open a session, send ``request`` with the open, wait for its outcome, and close it.

        ``timeout`` bounds the wait for the outcome, in seconds, once the session is ready.
        """
        session, exchange = self._open(agent_name, idempotency_key, ready_timeout, request)
        try:
            # Present whenever a first input is given.
            return cast(Exchange, exchange).wait_for_outcome(timeout)
        finally:
            session.close()

    def destroy(self) -> None:
        """Close every session, including any still opening, without waiting; stop credential
        refresh. Later opens are refused with ``session_canceled``."""
        with self._lock:
            self._destroyed = True
            pending = list(self._pending_opens)
            sessions = list(self._sessions)
        for abandon in pending:
            abandon.set()
        for session in sessions:
            # Now, not when the close thread starts: a session_ready landing first must still
            # fail its open.
            session._request_close()
            spawn(lambda s=session: self._close_quietly(s), "blocks-session-close")
        if self._config.owned_auth is not None:
            self._config.owned_auth.destroy()

    def __enter__(self) -> "BlocksClient":
        return self

    def __exit__(self, *_: Any) -> None:
        self.destroy()

    # -- internals ----------------------------------------------------------------------

    @staticmethod
    def _close_quietly(session: Session) -> None:
        try:
            session.close()
        except Exception as err:
            logger.warning("session close on destroy failed: %s", err)

    def _open(
        self,
        agent_name: str,
        idempotency_key: Optional[str],
        ready_timeout: float,
        first_input: Optional[SessionInput] = None,
    ) -> Tuple[Session, Optional[Exchange]]:
        first_parts = None if first_input is None else to_request_parts(first_input)
        _check_idempotency_key(idempotency_key)
        ready = ready_deadline(ready_timeout)
        if self._destroyed:
            raise _destroyed_error()
        key = idempotency_key
        if key is None:
            return self._open_once(agent_name, None, ready, first_parts, None)
        with self._lock:
            if key in self._keys_held:
                raise SessionError(
                    ALREADY_OPEN,
                    f"idempotency_key {key} already names a session on this client; "
                    "open again under a new key",
                )
            self._keys_held.add(key)
        hold = _KeyHold(free=lambda: self._free_key(key))
        try:
            return self._open_once(agent_name, key, ready, first_parts, hold)
        except BaseException:
            # A session created before the failure is still tearing down, and its release
            # frees the key; an abandoned request may yet open one, so the key waits for it.
            if not hold.created:
                if hold.abandoned is None:
                    hold.free()
                else:
                    hold.abandoned.add_done_callback(lambda _: hold.free())
            raise

    def _free_key(self, key: str) -> None:
        with self._lock:
            self._keys_held.discard(key)

    def _open_once(
        self,
        agent_name: str,
        key: Optional[str],
        ready: ReadyDeadline,
        first_parts: Optional[List[Dict[str, Any]]],
        hold: Optional[_KeyHold],
    ) -> Tuple[Session, Optional[Exchange]]:
        config = self._config
        opened = self._request_open(agent_name, key, ready, hold)
        if opened.idempotent and opened.state != wire.STATE_OPEN:
            if self._destroyed:
                raise _destroyed_error()
            # Another client holds this session past its open, under the same transport
            # identity and its own seq, so joining it would collide; closing it would end it
            # for that holder.
            raise SessionError(
                ALREADY_OPEN,
                f"idempotency_key {key} names a session that is already {opened.state}; "
                "open again under a new key",
            )
        if self._destroyed:
            # destroy() ran during the open request: the backend holds a session nobody uses,
            # unless it is a replay, whose session may be another holder's.
            if not opened.idempotent:
                self._beacon_closed(opened)
            raise _destroyed_error()
        pubnub = create_pubnub_client(
            subscribe_key=config.subscribe_key,
            publish_key=config.publish_key,
            user_id=opened.consumer_transport_uuid,
            subscribe_retry_unbounded=True,
        )
        scheduler, clock = _default_timing()

        def created(session: Session) -> None:
            with self._lock:
                self._sessions.add(session)
                # destroy() takes the same lock, so it either saw this session or set this flag.
                destroyed = self._destroyed
            if hold is not None:
                hold.created = True
            if destroyed:
                session._request_close()
                spawn(lambda: self._close_quietly(session), "blocks-session-close")

        def released(session: Session) -> None:
            with self._lock:
                self._sessions.discard(session)
            if hold is not None:
                hold.free()

        return Session._open(
            opened,
            SessionTransport(pubnub, config.http, scheduler, clock),
            SessionLifecycle(created, released),
            ready=ready,
            first_parts=first_parts,
        )

    def _request_open(
        self,
        agent_name: str,
        key: Optional[str],
        ready: ReadyDeadline,
        hold: Optional[_KeyHold],
    ) -> OpenedSession:
        """``POST /api/v1/sessions``, abandoned when the ready deadline passes or the client
        is destroyed. Auth preparation runs inside it, so a stalled refresh is bounded too."""
        http = self._config.http
        wake = threading.Event()
        with self._lock:
            if self._destroyed:
                raise _destroyed_error()
            self._pending_opens.add(wake)
        try:
            opening = spawn(lambda: http.open_session(agent_name, key), "blocks-session-open")
            opening.add_done_callback(lambda _: wake.set())
            wake.wait(ready.remaining())
            if opening.done():
                return opening.result()
            # An open that completes after being abandoned still leaves a session on the
            # backend. A replay names a session someone else may hold, even one still open
            # while its holder waits for the agent, so it is left alone.
            settled: "Future[None]" = Future()

            def late(done: "Future[OpenedSession]") -> None:
                try:
                    if done.exception() is None and not done.result().idempotent:
                        self._config.http.send_beacon(done.result().session_id, BEACON_CLOSED)
                except Exception as err:
                    logger.debug("session beacon closed failed: %s", err)
                finally:
                    settled.set_result(None)

            opening.add_done_callback(late)
            if hold is not None:
                hold.abandoned = settled
            raise _destroyed_error() if self._destroyed else ready_timeout_error(ready)
        finally:
            with self._lock:
                self._pending_opens.discard(wake)

    def _beacon_closed(self, opened: OpenedSession) -> None:
        def send() -> None:
            try:
                self._config.http.send_beacon(opened.session_id, BEACON_CLOSED)
            except Exception as err:
                logger.debug("session beacon closed failed: %s", err)

        spawn(send, "blocks-session-beacon")
