"""Delayed callbacks for the receivers, injectable so tests advance time instead of sleeping."""

from __future__ import annotations

import logging
import threading
from typing import Callable, Protocol

logger = logging.getLogger(__name__)


class Cancellable(Protocol):
    def cancel(self) -> None: ...


class Scheduler(Protocol):
    def call_later(self, delay_s: float, callback: Callable[[], None]) -> Cancellable: ...


class ThreadingScheduler:
    def call_later(self, delay_s: float, callback: Callable[[], None]) -> Cancellable:
        def run() -> None:
            try:
                callback()
            except Exception:
                logger.exception(
                    "session timer %s raised", getattr(callback, "__qualname__", callback)
                )

        timer = threading.Timer(delay_s, run)
        timer.daemon = True
        timer.start()
        return timer
