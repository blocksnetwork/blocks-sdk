"""Consumer session token rotation, and the one refresh a PubNub access denial earns."""

from __future__ import annotations

import logging
import threading
from typing import Callable, Optional

from .http import SessionToken, is_refusal
from .scheduler import Cancellable, Scheduler
from .token_schedule import TOKEN_REFRESH_AT, TOKEN_RETRY_MAX_S, TOKEN_RETRY_MIN_S

logger = logging.getLogger(__name__)



class ConsumerTokenRotation:
    """Rotates a consumer session token at 80% of its TTL, and refreshes it once on a denial.

    A rotation that fails in transit retries with backoff until the token expires; a
    refusal is final. After a denial exactly one refresh runs: on success the token is
    applied and ``resume`` reconnects the parked subscription; on failure, or on a second
    denial before the subscription reconnects, ``on_revoked`` fails the session.
    """

    def __init__(
        self,
        *,
        session_id: str,
        refresh: Callable[[], SessionToken],
        set_token: Callable[[str], None],
        resume: Callable[[], None],
        on_revoked: Callable[[], None],
        scheduler: Scheduler,
        clock: Callable[[], float],
    ) -> None:
        self._session_id = session_id
        self._refresh = refresh
        self._set_token = set_token
        self._resume = resume
        self._on_revoked = on_revoked
        self._scheduler = scheduler
        self._clock = clock
        self._lock = threading.Lock()
        self._timer: Optional[Cancellable] = None
        self._retry_s = TOKEN_RETRY_MIN_S
        self._expires_at = 0.0
        self._denial_refresh = False
        # A denial refresh succeeded and the subscription has not reconnected since.
        self._awaiting_reconnect = False
        self._stopped = False

    def start(self, ttl_minutes: float) -> None:
        with self._lock:
            if not self._stopped:
                self._schedule(ttl_minutes)

    def stop(self) -> None:
        with self._lock:
            self._stopped = True
            if self._timer is not None:
                self._timer.cancel()
                self._timer = None

    def on_connected(self) -> None:
        with self._lock:
            self._awaiting_reconnect = False

    def on_denied(self) -> None:
        with self._lock:
            if self._stopped or self._denial_refresh:
                return
            revoke = self._awaiting_reconnect
            if not revoke:
                self._denial_refresh = True
        if revoke:
            self._revoke()
        else:
            # Off the caller's thread: a denial is reported on PubNub's subscribe thread.
            self._scheduler.call_later(0, self._refresh_after_denial)

    def _refresh_after_denial(self) -> None:
        try:
            token = self._refresh()
        except Exception as err:
            with self._lock:
                self._denial_refresh = False
                stopped = self._stopped
            if not stopped:
                logger.warning(
                    "session access refresh after a denial failed (session %s): %s",
                    self._session_id,
                    err,
                )
                self._revoke()
            return
        with self._lock:
            self._denial_refresh = False
            if self._stopped:
                return
            self._apply(token)
            self._awaiting_reconnect = True
        self._resume()

    def _revoke(self) -> None:
        self.stop()
        self._on_revoked()

    def _apply(self, token: SessionToken) -> None:
        self._set_token(token.pam_token)
        self._retry_s = TOKEN_RETRY_MIN_S
        self._schedule(token.ttl_minutes)

    def _schedule(self, ttl_minutes: float) -> None:
        ttl_s = ttl_minutes * 60
        self._expires_at = self._clock() + ttl_s
        self._set_timer(ttl_s * TOKEN_REFRESH_AT)

    def _set_timer(self, delay_s: float) -> None:
        if self._timer is not None:
            self._timer.cancel()
        self._timer = self._scheduler.call_later(delay_s, self._rotate)

    def _rotate(self) -> None:
        try:
            token = self._refresh()
        except Exception as err:
            with self._lock:
                if self._stopped:
                    return
                # A refusal is final: the token lapses or is revoked, and the denial then ends
                # the session.
                retry = not is_refusal(err) and self._clock() + self._retry_s < self._expires_at
                logger.warning(
                    "session access refresh failed; %s (session %s): %s",
                    "retrying" if retry else "giving up",
                    self._session_id,
                    err,
                )
                if retry:
                    self._set_timer(self._retry_s)
                    self._retry_s = min(self._retry_s * 2, TOKEN_RETRY_MAX_S)
            return
        with self._lock:
            if not self._stopped:
                self._apply(token)
