"""Layer-1 dedup for terminal events (Python parity with Node).

Cooperative cancel and the scanner's safety net can both produce a
terminal for the same task. The DB layer (``taskFanout`` PubNub Function)
already dedupes at the row-status level, but consumers subscribed to the
task channel still see two wire-level terminals. This tracker enforces
exactly-once delivery to every public terminal callback surface so SDK
consumers never have to defend against the duplicate themselves.

The tracker is single-shot: the *first* terminal observed wins; any later
terminal -- different state, different reason, different source -- is
silently dropped. The user's intent (cancel) wins over a stale agent
terminal because the scanner publishes after the cancel-budget has elapsed.

Mirror of ``blocks-sdk/sdks/node/src/runtime/terminal-delivery-tracker.ts``.
"""

from __future__ import annotations

from typing import Any, Callable, Dict, Optional


class TerminalDeliveryTracker:
    """First-terminal-wins gate for one TaskSession or subscription scope."""

    def __init__(self) -> None:
        self._delivered = False
        self._first_event: Optional[Dict[str, Any]] = None

    def try_deliver(
        self,
        evt: Dict[str, Any],
        on_deliver: Callable[[Dict[str, Any]], None],
    ) -> bool:
        """Deliver the event through ``on_deliver`` if no terminal has been
        delivered yet.

        Returns True when the event was the first and was delivered;
        False when a terminal had already been delivered and the event was
        dropped. The ``delivered`` flag is set *before* the callback runs
        so the callback sees ``is_delivered is True`` (re-entrant safety:
        prevents recursive delivery via callback side-effects).
        """
        if self._delivered:
            return False
        self._delivered = True
        self._first_event = evt
        on_deliver(evt)
        return True

    def peek(self) -> Optional[Dict[str, Any]]:
        """Return the first delivered terminal, or ``None`` if none has
        been delivered yet. Used by ``on_terminal`` and
        ``wait_for_terminal`` to synchronously hand a previously-delivered
        terminal to a freshly registered observer.
        """
        return self._first_event

    @property
    def is_delivered(self) -> bool:
        return self._delivered
