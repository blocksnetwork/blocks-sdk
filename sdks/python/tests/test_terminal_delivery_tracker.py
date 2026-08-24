"""Unit tests for TerminalDeliveryTracker.

Mirror of blocks-sdk/sdks/node/tests/terminal-delivery-tracker.test.ts.
"""
from blocks_network.terminal_delivery_tracker import TerminalDeliveryTracker


def _evt(state: str) -> dict:
    return {"type": "terminal", "taskId": "t1", "state": state}


def test_peek_returns_none_and_is_delivered_false_on_fresh_tracker() -> None:
    t = TerminalDeliveryTracker()
    assert t.peek() is None
    assert t.is_delivered is False


def test_first_try_deliver_invokes_callback_and_returns_true() -> None:
    t = TerminalDeliveryTracker()
    seen: list = []
    result = t.try_deliver(_evt("canceled"), seen.append)
    assert result is True
    assert seen == [_evt("canceled")]
    assert t.is_delivered is True


def test_subsequent_try_deliver_does_not_invoke_callback_and_returns_false() -> None:
    t = TerminalDeliveryTracker()
    t.try_deliver(_evt("canceled"), lambda _: None)
    seen: list = []
    result = t.try_deliver(_evt("completed"), seen.append)
    assert result is False
    assert seen == []


def test_peek_after_first_delivery_returns_first_event() -> None:
    t = TerminalDeliveryTracker()
    t.try_deliver(_evt("canceled"), lambda _: None)
    t.try_deliver(_evt("completed"), lambda _: None)
    assert t.peek() == _evt("canceled")


def test_marks_delivered_before_invoking_callback() -> None:
    """Re-entrant safety: callback sees is_delivered == True."""
    t = TerminalDeliveryTracker()
    observed_during_callback = [False]

    def _cb(_: dict) -> None:
        observed_during_callback[0] = t.is_delivered

    t.try_deliver(_evt("canceled"), _cb)
    assert observed_during_callback[0] is True
