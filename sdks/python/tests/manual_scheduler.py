"""A Scheduler whose time moves only when a test calls ``advance``."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Callable, List


@dataclass
class _Pending:
    due_ms: float
    callback: Callable[[], None]
    canceled: bool = field(default=False)

    def cancel(self) -> None:
        self.canceled = True


class ManualScheduler:
    def __init__(self) -> None:
        self.now_ms = 0.0
        self._pending: List[_Pending] = []

    def call_later(self, delay_s: float, callback: Callable[[], None]) -> _Pending:
        pending = _Pending(self.now_ms + delay_s * 1000, callback)
        self._pending.append(pending)
        return pending

    def advance(self, ms: float) -> None:
        target = self.now_ms + ms
        while True:
            due = [p for p in self._pending if not p.canceled and p.due_ms <= target]
            if not due:
                break
            nxt = min(due, key=lambda p: p.due_ms)
            self._pending.remove(nxt)
            self.now_ms = nxt.due_ms
            nxt.callback()
        self.now_ms = target
