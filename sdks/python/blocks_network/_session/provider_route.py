"""The provider's session route: registration, subscription, token upkeep and replacement."""

from __future__ import annotations

import logging
import math
import time
import threading
from collections import OrderedDict
from dataclasses import dataclass, replace
from typing import Any, Callable, Dict, List, Optional, Tuple

from . import wire
from .counterpart import InboundKind, classify_inbound, provider_counterparts
from .errors import ROUTE_LOST, SESSION_NOT_FOUND
from .provider_api import (
    TASK_ONLY_CODES,
    ProviderApiError,
    ProviderSessionApi,
    RouteGrant,
    SessionManifest,
    error_code_of,
    parse_manifest,
)
from .provider_checkpoints import CHECKPOINT_MAX_OPEN_SESSIONS, CheckpointLoop
from .provider_session import MAX_PENDING_EXCHANGES, ProviderSession, spawn_daemon
from .provider_transport import RouteClient
from .provider_types import SessionHandlers
from .scheduler import Cancellable, Scheduler, ThreadingScheduler
from .token_schedule import (
    TOKEN_REFRESH_AT,
    TOKEN_RETRY_MAX_S,
    TOKEN_RETRY_MIN_S,
    is_refusal_status,
    rate_limited_wait_s,
)

logger = logging.getLogger(__name__)

ROUTE_RETRY_MIN_S = 1.0
ROUTE_RETRY_MAX_S = 60.0
# A route that cannot subscribe in this long is replaced rather than left unoffered.
ROUTE_CONNECT_TIMEOUT_S = 10.0
_MANIFEST_CACHE_LIMIT = 256
_LOOKUP_RETRY_DELAYS_S = (0.0, 0.25, 1.0, 3.0)
_ENDED_SESSION_LIMIT = 1024
_MAX_SESSIONS_RANGE = (1, CHECKPOINT_MAX_OPEN_SESSIONS)
_OPENING_BUFFER_LIMIT = MAX_PENDING_EXCHANGES * 2

Buffered = List[Tuple[Optional[str], Any]]


def _session_open_for(message: Any, session_id: str) -> bool:
    return (
        isinstance(message, dict)
        and message.get("type") == wire.SESSION_OPEN
        and message.get("sessionId") == session_id
    )


def _is_route_probe(message: Any) -> bool:
    """A route probe is recognized by type wherever it arrives, and never reaches a session."""
    return isinstance(message, dict) and message.get("type") == wire.ROUTE_PROBE


def clamp_max_sessions(requested: Any) -> int:
    low, high = _MAX_SESSIONS_RANGE
    is_number = isinstance(requested, (int, float)) and not isinstance(requested, bool)
    if not is_number or not math.isfinite(requested):
        return low
    return min(max(math.floor(requested), low), high)


@dataclass(frozen=True)
class _RouteDeps:
    api: ProviderSessionApi
    max_sessions: int
    handlers: SessionHandlers
    create_client: Callable[[str], RouteClient]
    take_manifest: Callable[[str], Optional[SessionManifest]]
    on_lost: Callable[[], None]
    on_connected: Callable[[], None]
    # The route never subscribed; the runtime retries on its backoff rather than at once.
    on_connect_timeout: Callable[[], None]
    scheduler: Scheduler
    clock: Callable[[], float]


class _SessionRoute:
    """Lock order: route, receiver, session, checkpoints; ``_deps`` is never called locked."""

    def __init__(self, grant: RouteGrant, deps: _RouteDeps) -> None:
        self._grant = grant
        self._deps = deps
        self._lock = threading.RLock()
        self._sessions: Dict[str, ProviderSession] = {}
        self._opening: Dict[str, Buffered] = {}
        self._ended: "OrderedDict[str, None]" = OrderedDict()
        self._prefix = (
            grant.route_channel[:-1] if grant.route_channel.endswith("*") else (grant.route_channel)
        )
        self._client = deps.create_client(grant.provider_transport_uuid)
        try:
            self._client.set_token(grant.pam_token)
        except Exception:
            self._close_client()
            raise
        self._checkpoints = CheckpointLoop(
            route_id=grant.route_id,
            post=deps.api.post_checkpoint,
            open_sessions=self._live_session_ids,
            on_route_lost=deps.on_lost,
            scheduler=deps.scheduler,
            spawn=spawn_daemon,
        )
        self._connect_timer: Optional[Cancellable] = None
        self._token_timer: Optional[Cancellable] = None
        self._token_retry_s = TOKEN_RETRY_MIN_S
        self._heartbeating = False
        self._refreshing_on_denial = False
        # A denial refresh succeeded and the subscription has not reconnected since.
        self._refreshed_since_connect = False
        self._closed = False

    @property
    def route_id(self) -> str:
        return self._grant.route_id

    def start(self) -> bool:
        """Heartbeats wait for the subscription: the first acknowledged one offers the route.
        False when the route was closed before it subscribed."""
        with self._lock:
            if self._closed:
                return False
            self._connect_timer = self._deps.scheduler.call_later(
                ROUTE_CONNECT_TIMEOUT_S, self._on_connect_timeout
            )
        self._schedule_token_refresh(self._grant.ttl_minutes)
        self._client.listen(self._on_message, self._on_status)
        if self._closed_meanwhile():
            return False
        self._client.subscribe(self._grant.route_channel)
        return not self._closed_meanwhile()

    def _closed_meanwhile(self) -> bool:
        """A close that ran before this point tore down a client that has since grown a
        listener or a subscription, so tear it down again."""
        with self._lock:
            closed = self._closed
        if closed:
            self._close_client()
        return closed

    def close(self, reason: str) -> None:
        with self._lock:
            if self._closed:
                return
            self._closed = True
            sessions = list(self._sessions.values())
            self._sessions.clear()
            self._opening.clear()
            for timer in (self._token_timer, self._connect_timer):
                if timer is not None:
                    timer.cancel()
        self._checkpoints.stop()
        for session in sessions:
            session.end_locally(reason)
        self._close_client()

    def _close_client(self) -> None:
        try:
            self._client.close(self._grant.route_channel)
        except Exception:
            logger.debug("route client was already torn down", exc_info=True)

    def _on_connect_timeout(self) -> None:
        with self._lock:
            if self._closed or self._heartbeating:
                return
        logger.warning("session route %s did not subscribe in time; retrying", self.route_id)
        self._deps.on_connect_timeout()

    def _on_status(self, category: str) -> None:
        with self._lock:
            if self._closed:
                return
            if category == "access_denied":
                if self._refreshing_on_denial:
                    return
                if not self._refreshed_since_connect:
                    self._refreshing_on_denial = True
                    spawn_daemon(self._refresh_on_denial)
                    return
                lost = True
            else:
                lost = False
                if category in ("connected", "reconnected"):
                    self._refreshed_since_connect = False
                if self._heartbeating or category != "connected":
                    return
                self._heartbeating = True
                if self._connect_timer is not None:
                    self._connect_timer.cancel()
        if lost:
            # Denied again before reconnecting: its heartbeats would keep a deaf route offered.
            logger.warning(
                "route %s access denied again after a refresh; replacing the route", self.route_id
            )
            spawn_daemon(self._deps.on_lost)
            return
        self._deps.on_connected()
        self._checkpoints.start()
        self._checkpoints.flush()

    def _on_message(self, channel: Optional[str], publisher: Optional[str], message: Any) -> None:
        if not isinstance(channel, str) or not isinstance(publisher, str):
            return
        with self._lock:
            if self._closed:
                return
            if _is_route_probe(message):
                if publisher == self._grant.backend_transport_uuid:
                    self._checkpoints.echo_probe(message.get("nonce"))
                return
            if not channel.startswith(self._prefix):
                return
            session_id = channel[len(self._prefix) :]
            session = self._sessions.get(session_id)
            if session is not None:
                # Under the lock, so a message cannot overtake those buffered while opening.
                session.receive(publisher, message)
                return
            if session_id in self._ended:
                return
            buffered = self._opening.get(session_id)
            if buffered is not None:
                if len(buffered) < _OPENING_BUFFER_LIMIT:
                    buffered.append((publisher, message))
                return
            if not _session_open_for(message, session_id):
                return
            self._opening[session_id] = [(publisher, message)]
        spawn_daemon(lambda: self._open_session(session_id, channel))

    def _open_session(self, session_id: str, channel: str) -> None:
        manifest = self._resolve_manifest(session_id)
        rejected: Optional[ProviderSession] = None
        with self._lock:
            buffered = self._opening.pop(session_id, [])
            if not buffered or manifest is None or self._closed:
                return
            opener, open_message = buffered[0]
            backend_uuid = manifest.backend_transport_uuid or self._grant.backend_transport_uuid
            counterparts = provider_counterparts(
                session_id=session_id,
                consumer_uuid=manifest.consumer_transport_uuid,
                backend_uuid=backend_uuid,
            )
            # An opener the session would drop must not take a seat it could never release.
            if (
                classify_inbound(counterparts, opener, open_message).kind
                is not InboundKind.SEQUENCED
            ):
                return
            session = ProviderSession(
                manifest=replace(manifest, backend_transport_uuid=backend_uuid),
                channel=channel,
                client=self._client,
                handlers=self._deps.handlers,
                record=self._checkpoints.record,
                on_ended=lambda: self._mark_ended(session_id),
                scheduler=self._deps.scheduler,
                clock=self._deps.clock,
            )
            if len(self._live_session_ids()) >= self._deps.max_sessions:
                rejected = session
                self._mark_ended(session_id)
            else:
                self._sessions[session_id] = session
                for publisher, message in buffered:
                    session.receive(publisher, message)
        if rejected is not None:
            rejected.reject_at_capacity()

    def _live_session_ids(self) -> List[str]:
        """Ending sessions hold no seat and are not listed, or the backend would re-send notices."""
        with self._lock:
            return [sid for sid, session in self._sessions.items() if not session.ending]

    def _mark_ended(self, session_id: str) -> None:
        with self._lock:
            self._sessions.pop(session_id, None)
            self._ended[session_id] = None
            if len(self._ended) > _ENDED_SESSION_LIMIT:
                self._ended.popitem(last=False)

    def _resolve_manifest(self, session_id: str) -> Optional[SessionManifest]:
        manifest = self._deps.take_manifest(session_id) or self._look_up_session(session_id)
        if manifest is None:
            return None
        matches = manifest.session_id == session_id and manifest.route_id == self.route_id
        return manifest if matches else None

    def _look_up_session(self, session_id: str) -> Optional[SessionManifest]:
        """Retry a failed lookup with backoff; only ``SESSION_NOT_FOUND`` is final at once."""
        last = len(_LOOKUP_RETRY_DELAYS_S) - 1
        for attempt, delay_s in enumerate(_LOOKUP_RETRY_DELAYS_S):
            if delay_s > 0:
                self._sleep(delay_s)
            if self._closed:
                return None
            try:
                return self._deps.api.get_session(session_id)
            except Exception as exc:
                if error_code_of(exc) == SESSION_NOT_FOUND:
                    return None
                logger.warning(
                    "session %s lookup failed; %s: %s",
                    session_id,
                    "dropping session_open" if attempt == last else "retrying",
                    exc,
                )
        return None

    def _sleep(self, delay_s: float) -> None:
        woken = threading.Event()
        self._deps.scheduler.call_later(delay_s, woken.set)
        woken.wait()

    def _schedule_token_refresh(self, ttl_minutes: float) -> None:
        self._set_token_timer(ttl_minutes * 60 * TOKEN_REFRESH_AT)

    def _set_token_timer(self, delay_s: float) -> None:
        with self._lock:
            if self._closed:
                return
            if self._token_timer is not None:
                self._token_timer.cancel()
            self._token_timer = self._deps.scheduler.call_later(delay_s, self._refresh_token)

    def _refresh_on_denial(self) -> None:
        try:
            token = self._deps.api.refresh_route_token(self.route_id)
        except Exception as exc:
            with self._lock:
                self._refreshing_on_denial = False
                if self._closed:
                    return
            logger.warning(
                "route %s access denied and not renewed; replacing the route: %s",
                self.route_id,
                exc,
            )
            self._deps.on_lost()
            return
        try:
            with self._lock:
                if self._closed:
                    return
                self._refreshed_since_connect = True
                # Cleared before reconnecting, so a denial while it reconnects replaces the route.
                self._refreshing_on_denial = False
            self._client.set_token(token.pam_token)
            self._schedule_token_refresh(token.ttl_minutes)
            # pubnub-python drops every subscription on a denial, so resubscribe explicitly.
            self._client.subscribe(self._grant.route_channel)
            self._closed_meanwhile()
        except Exception as exc:
            logger.warning(
                "route %s did not resume after a denial; replacing the route: %s",
                self.route_id,
                exc,
            )
            self._deps.on_lost()

    def _refresh_token(self) -> None:
        try:
            token = self._deps.api.refresh_route_token(self.route_id)
        except Exception as exc:
            if self._closed:
                return
            refused = isinstance(exc, ProviderApiError) and is_refusal_status(exc.status)
            if error_code_of(exc) == ROUTE_LOST or refused:
                self._deps.on_lost()
                return
            logger.warning("route %s grant renewal failed; retrying: %s", self.route_id, exc)
            self._set_token_timer(max(self._token_retry_s, rate_limited_wait_s(exc)))
            self._token_retry_s = min(self._token_retry_s * 2, TOKEN_RETRY_MAX_S)
            return
        if self._closed:
            return
        self._client.set_token(token.pam_token)
        self._token_retry_s = TOKEN_RETRY_MIN_S
        self._schedule_token_refresh(token.ttl_minutes)


class SessionProviderRuntime:
    """Keeps one session route open for this instance. Experimental."""

    def __init__(
        self,
        *,
        api: ProviderSessionApi,
        instance_id: str,
        max_sessions: Any,
        handlers: SessionHandlers,
        create_client: Callable[[str], RouteClient],
        scheduler: Optional[Scheduler] = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._api = api
        self._instance_id = instance_id
        self._max_sessions = clamp_max_sessions(max_sessions)
        self._handlers = handlers
        self._create_client = create_client
        self._scheduler = scheduler or ThreadingScheduler()
        self._clock = clock
        self._lock = threading.RLock()
        self._route: Optional[_SessionRoute] = None
        self._manifests: "OrderedDict[str, SessionManifest]" = OrderedDict()
        self._retry_timer: Optional[Cancellable] = None
        self._retry_delay_s = ROUTE_RETRY_MIN_S
        self._stopped = False

    def start(self) -> None:
        """Return once the first route is open, or the instance is known to be task-only."""
        self._open_route()

    def accept_manifest(self, raw: Any) -> None:
        manifest = parse_manifest(raw)
        if manifest is None:
            return
        with self._lock:
            self._manifests.pop(manifest.session_id, None)
            self._manifests[manifest.session_id] = manifest
            if len(self._manifests) > _MANIFEST_CACHE_LIMIT:
                self._manifests.popitem(last=False)

    def stop(self) -> None:
        with self._lock:
            self._stopped = True
            if self._retry_timer is not None:
                self._retry_timer.cancel()
            route, self._route = self._route, None
        if route is not None:
            route.close("route_lost")

    def _open_route(self) -> None:
        if self._stopped:
            return
        try:
            grant = self._api.register_route(self._instance_id, self._max_sessions)
        except Exception as exc:
            code = error_code_of(exc)
            if code in TASK_ONLY_CODES:
                logger.info(
                    "sessions are not available for this agent (%s); serving tasks only", code
                )
                return
            delay_s = max(self._retry_delay_s, rate_limited_wait_s(exc))
            logger.warning("session route registration failed; retrying in %ss: %s", delay_s, exc)
            self._schedule_retry(delay_s)
            return
        try:
            route = self._new_route(grant)
        except Exception as exc:
            # The registered route is left for the backend to expire unheartbeated.
            logger.warning(
                "session route %s could not be created; retrying in %ss: %s",
                grant.route_id,
                self._retry_delay_s,
                exc,
            )
            self._schedule_retry()
            return
        with self._lock:
            discard = self._stopped
            if not discard:
                self._route = route
        if discard:
            route.close("route_lost")
            return
        try:
            started = route.start()
        except Exception as exc:
            logger.warning("session route %s did not start; retrying: %s", grant.route_id, exc)
            self._retry_route(route)
            return
        if started:
            logger.info("session route open: %s", grant.route_id)

    def _new_route(self, grant: RouteGrant) -> _SessionRoute:
        # The backoff resets on subscribe, not here, so a subscribe outage still backs off.
        route = _SessionRoute(
            grant,
            _RouteDeps(
                api=self._api,
                max_sessions=self._max_sessions,
                handlers=self._handlers,
                create_client=self._create_client,
                take_manifest=self._take_manifest,
                on_lost=lambda: self._replace_route(route),
                on_connected=self._on_connected,
                on_connect_timeout=lambda: self._retry_route(route),
                scheduler=self._scheduler,
                clock=self._clock,
            ),
        )
        return route

    def _on_connected(self) -> None:
        with self._lock:
            self._retry_delay_s = ROUTE_RETRY_MIN_S

    def _replace_route(self, lost: _SessionRoute) -> None:
        with self._lock:
            if self._route is not lost:
                return
            self._route = None
        logger.warning("session route %s lost; opening a new one", lost.route_id)
        lost.close("route_lost")
        self._open_route()

    def _retry_route(self, unconnected: _SessionRoute) -> None:
        with self._lock:
            if self._route is not unconnected:
                return
            self._route = None
        unconnected.close("route_lost")
        self._schedule_retry()

    def _schedule_retry(self, delay_s: Optional[float] = None) -> None:
        with self._lock:
            if self._stopped:
                return
            delay_s = self._retry_delay_s if delay_s is None else delay_s
            self._retry_timer = self._scheduler.call_later(delay_s, self._open_route)
            self._retry_delay_s = min(self._retry_delay_s * 2, ROUTE_RETRY_MAX_S)

    def _take_manifest(self, session_id: str) -> Optional[SessionManifest]:
        with self._lock:
            return self._manifests.pop(session_id, None)
