"""Run blocking work on a daemon thread, so a caller can wait on it with a bound."""

from __future__ import annotations

import threading
from concurrent.futures import Future
from typing import Callable, TypeVar

T = TypeVar("T")


def spawn(work: Callable[[], T], name: str = "blocks-session") -> "Future[T]":
    """Start ``work`` on its own daemon thread and return its future.

    A daemon thread, not an executor: a request that never answers must not hold the
    interpreter open at exit.
    """
    future: "Future[T]" = Future()

    def run() -> None:
        if not future.set_running_or_notify_cancel():
            return
        try:
            future.set_result(work())
        except BaseException as err:  # noqa: BLE001 - handed to whoever waits
            future.set_exception(err)

    threading.Thread(target=run, name=name, daemon=True).start()
    return future
