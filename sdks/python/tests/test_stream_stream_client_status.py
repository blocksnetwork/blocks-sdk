"""
Tests for StreamClient.on_error / status-error surfacing (Fix C, t7c).

Classifier + dispatch + forced-termination coverage. Parity with the
Node test file ``stream-stream-client-status.test.ts``.

Covers:
- ``_is_status_error`` classifier using ``PNStatus.is_error()`` as the
  primary signal, with fatal-allowlist fallback for older SDK shapes.
- ``_is_fatal_category`` fatal-allowlist membership.
- Dispatch: fatal category fires ``on_error`` with ``fatal=True``,
  forces termination, iterator exits cleanly.
- Dispatch: benign category (``PNConnectedCategory``) does not fire
  ``on_error``; stream remains active.
- Dispatch: non-fatal error category fires ``on_error`` with
  ``fatal=False``; stream remains active.
- Robustness: a consumer callback that throws does not break the forced
  termination path.
"""

from __future__ import annotations

from types import SimpleNamespace
from typing import Any, List
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.stream.stream_client import (
    FATAL_STREAM_ERROR_CATEGORIES,
    StreamClient,
    StreamError,
    _coerce_category_name,
    _is_fatal_category,
    _is_status_error,
    _reset_uuid_counter,
)


# -- Fixtures --------------------------------------------------------------


@pytest.fixture(autouse=True)
def _reset_counter():
    _reset_uuid_counter()
    yield
    _reset_uuid_counter()


class _FakeConfig:
    def __init__(self):
        self.subscribe_key = None
        self.publish_key = None
        self.user_id = None


def _make_mock_pubnub() -> MagicMock:
    """Minimal PubNub instance mock used by the stream client."""
    instance = MagicMock()
    instance.set_token = MagicMock()

    # Subscribe / unsubscribe builder chains.
    subscribe_execute = MagicMock()
    subscribe_with_tt = MagicMock(return_value=MagicMock(execute=subscribe_execute))
    subscribe_channels = MagicMock(return_value=MagicMock(with_timetoken=subscribe_with_tt))
    instance.subscribe.return_value = MagicMock(channels=subscribe_channels)

    unsubscribe_execute = MagicMock()
    unsubscribe_channels = MagicMock(return_value=MagicMock(execute=unsubscribe_execute))
    instance.unsubscribe.return_value = MagicMock(channels=unsubscribe_channels)

    # Publish builder chain (for end() -> stream_end marker path).
    publish_sync = MagicMock()
    publish_should_store = MagicMock(return_value=MagicMock(sync=publish_sync))
    publish_meta = MagicMock(return_value=MagicMock(should_store=publish_should_store))
    publish_message = MagicMock(return_value=MagicMock(meta=publish_meta))
    publish_channel = MagicMock(return_value=MagicMock(message=publish_message))
    instance.publish.return_value = MagicMock(channel=publish_channel)

    instance.add_listener = MagicMock()
    instance.remove_listener = MagicMock()
    instance.unsubscribe_all = MagicMock()
    instance.stop = MagicMock()
    return instance


@pytest.fixture
def mock_pubnub():
    with patch("blocks_network.stream.stream_client.PubNub") as mock_cls, \
         patch("blocks_network.stream.stream_client.PNConfiguration", return_value=_FakeConfig()):
        mock_cls.side_effect = lambda cfg: _make_mock_pubnub()
        yield mock_cls


def _make_inbound_client() -> StreamClient:
    return StreamClient(
        subscribe_key="sub-key",
        publish_key="pub-key",
        token="test-token",
        agent_name="test_agent",
        stream_id="my-stream",
        direction="inbound",
    )


def _get_registered_listener(client: StreamClient):
    """Return the SubscribeCallback instance registered on the PubNub mock."""
    assert client._message_listener is not None
    return client._message_listener


class _StatusWithIsError:
    """Mimics the real ``PNStatus`` surface used by pubnub-python."""

    def __init__(
        self,
        *,
        category: Any = None,
        error: Any = None,
        error_data: Any = None,
    ) -> None:
        self.category = category
        self.error = error
        self.error_data = error_data

    def is_error(self) -> bool:
        return bool(self.error)


class _StatusNoIsError:
    """Older/alternate SDK shape: no ``is_error()`` method — forces the
    classifier into its fallback path (fatal-allowlist membership)."""

    def __init__(self, *, category: Any = None) -> None:
        self.category = category


# Mirrors the real pubnub-python ``PNStatusCategory`` surface: a plain
# ``enum.Enum`` (NOT a str subclass, NOT an IntEnum) whose ``name``
# attribute yields the canonical ``"PNAccessDeniedCategory"``-style
# string. Used to reproduce what the live PubNub SDK actually passes to
# SubscribeCallback.status() for 403 subscribe failures.
import enum as _enum  # noqa: E402  -- grouped with fixture for locality


class _FakePNStatusCategory(_enum.Enum):
    PNAccessDeniedCategory = 3
    PNBadRequestCategory = 4
    PNTimeoutCategory = 5
    PNNetworkIssuesCategory = 6
    PNConnectedCategory = 7


class _StatusWithEnumCategory:
    """Mimics real ``PNStatus`` where ``category`` is a PNStatusCategory
    enum member (the shape actually delivered by the installed
    pubnub-python SDK), not a raw string."""

    def __init__(
        self,
        *,
        category: _FakePNStatusCategory,
        error: Any = True,
        error_data: Any = None,
    ) -> None:
        self.category = category
        self.error = error
        self.error_data = error_data

    def is_error(self) -> bool:
        return bool(self.error)


# -- Classifier ------------------------------------------------------------


class TestIsStatusError:
    def test_true_when_is_error_returns_true(self):
        s = _StatusWithIsError(category="PNNetworkIssuesCategory", error=True)
        assert _is_status_error(s) is True

    def test_false_when_is_error_returns_false(self):
        s = _StatusWithIsError(category="PNConnectedCategory", error=False)
        assert _is_status_error(s) is False

    def test_fallback_on_missing_is_error_uses_fatal_allowlist(self):
        # No is_error() method — fall back to fatal-category membership.
        s = _StatusNoIsError(category="PNAccessDeniedCategory")
        assert _is_status_error(s) is True

    def test_fallback_rejects_non_fatal_category(self):
        s = _StatusNoIsError(category="PNNetworkIssuesCategory")
        assert _is_status_error(s) is False

    def test_fallback_rejects_unknown_category(self):
        s = _StatusNoIsError(category="PNBogusCategory")
        assert _is_status_error(s) is False

    def test_fallback_rejects_non_string_category(self):
        s = _StatusNoIsError(category=123)
        assert _is_status_error(s) is False

        s = _StatusNoIsError(category=None)
        assert _is_status_error(s) is False

    def test_is_error_raising_falls_through_gracefully(self):
        """If ``is_error()`` raises, classifier falls back to category
        membership rather than letting the error propagate."""
        class _Broken:
            category = "PNAccessDeniedCategory"

            def is_error(self):
                raise RuntimeError("boom")

        assert _is_status_error(_Broken()) is True  # fatal-allowlist save

        class _BrokenBenign:
            category = "PNConnectedCategory"

            def is_error(self):
                raise RuntimeError("boom")

        assert _is_status_error(_BrokenBenign()) is False

    def test_none_status(self):
        # Missing attributes — should not raise.
        s = SimpleNamespace()
        assert _is_status_error(s) is False

    # Non-fatal transport errors: MUST surface via on_error so consumers
    # can observe transient conditions, but MUST NOT force-terminate.
    # Shapes mirror what pubnub-python emits for transient network and
    # timeout conditions.
    def test_pn_timeout_category_with_error_true_is_detected(self):
        s = _StatusWithIsError(category="PNTimeoutCategory", error=True)
        assert _is_status_error(s) is True

    def test_pn_network_issues_category_with_error_true_is_detected(self):
        s = _StatusWithIsError(category="PNNetworkIssuesCategory", error=True)
        assert _is_status_error(s) is True

    def test_pn_connection_error_category_with_string_error_is_detected(self):
        s = _StatusWithIsError(
            category="PNConnectionErrorCategory", error="PNTimeoutCategory",
        )
        assert _is_status_error(s) is True

    def test_pn_disconnected_unexpectedly_category_with_string_error_is_detected(self):
        s = _StatusWithIsError(
            category="PNDisconnectedUnexpectedlyCategory", error="PNTimeoutCategory",
        )
        assert _is_status_error(s) is True

    # Category-only transport-state announcements: NOT errors.
    def test_pn_network_down_category_is_not_error(self):
        s = _StatusWithIsError(category="PNNetworkDownCategory", error=False)
        assert _is_status_error(s) is False

    def test_pn_network_up_category_is_not_error(self):
        s = _StatusWithIsError(category="PNNetworkUpCategory", error=False)
        assert _is_status_error(s) is False

    def test_pn_connected_category_is_not_error(self):
        s = _StatusWithIsError(category="PNConnectedCategory", error=False)
        assert _is_status_error(s) is False

    def test_pn_reconnected_category_is_not_error(self):
        s = _StatusWithIsError(category="PNReconnectedCategory", error=False)
        assert _is_status_error(s) is False

    # Real pubnub-python delivers ``category`` as a PNStatusCategory enum,
    # not a raw string. Classifier must still classify correctly.
    def test_pam_denied_with_real_enum_category_is_error(self):
        s = _StatusWithEnumCategory(
            category=_FakePNStatusCategory.PNAccessDeniedCategory, error=True,
        )
        assert _is_status_error(s) is True

    def test_is_error_fallback_recognizes_enum_fatal_category(self):
        # No is_error() method -> force fallback path, with enum category.
        class _EnumNoIsError:
            category = _FakePNStatusCategory.PNAccessDeniedCategory

        assert _is_status_error(_EnumNoIsError()) is True

    def test_is_error_fallback_rejects_enum_non_fatal_category(self):
        class _EnumNoIsError:
            category = _FakePNStatusCategory.PNTimeoutCategory

        assert _is_status_error(_EnumNoIsError()) is False


class TestCoerceCategoryName:
    def test_passes_through_string(self):
        assert _coerce_category_name("PNAccessDeniedCategory") == "PNAccessDeniedCategory"

    def test_extracts_name_from_enum(self):
        assert (
            _coerce_category_name(_FakePNStatusCategory.PNAccessDeniedCategory)
            == "PNAccessDeniedCategory"
        )

    def test_returns_empty_string_for_none(self):
        assert _coerce_category_name(None) == ""

    def test_returns_empty_string_for_non_string_non_enum(self):
        assert _coerce_category_name(123) == ""
        assert _coerce_category_name(object()) == ""


class TestIsFatalCategory:
    def test_accepts_pn_access_denied(self):
        assert _is_fatal_category("PNAccessDeniedCategory") is True

    def test_accepts_pn_bad_request(self):
        assert _is_fatal_category("PNBadRequestCategory") is True

    def test_rejects_non_fatal_error_categories(self):
        assert _is_fatal_category("PNNetworkIssuesCategory") is False
        assert _is_fatal_category("PNTimeoutCategory") is False
        assert _is_fatal_category("PNNetworkDownCategory") is False

    def test_rejects_benign_categories(self):
        assert _is_fatal_category("PNConnectedCategory") is False
        assert _is_fatal_category("PNReconnectedCategory") is False

    def test_rejects_empty_and_none(self):
        assert _is_fatal_category("") is False
        assert _is_fatal_category(None) is False

    def test_rejects_non_string(self):
        assert _is_fatal_category(123) is False

    # PNStatusCategory enum members must classify correctly: the installed
    # pubnub-python SDK delivers categories as enum members, not strings.
    def test_accepts_pn_access_denied_enum_member(self):
        assert (
            _is_fatal_category(_FakePNStatusCategory.PNAccessDeniedCategory) is True
        )

    def test_accepts_pn_bad_request_enum_member(self):
        assert (
            _is_fatal_category(_FakePNStatusCategory.PNBadRequestCategory) is True
        )

    def test_rejects_non_fatal_enum_member(self):
        assert (
            _is_fatal_category(_FakePNStatusCategory.PNTimeoutCategory) is False
        )
        assert (
            _is_fatal_category(_FakePNStatusCategory.PNConnectedCategory) is False
        )

    def test_fatal_set_is_exactly_allowlisted(self):
        assert FATAL_STREAM_ERROR_CATEGORIES == frozenset(
            {"PNAccessDeniedCategory", "PNBadRequestCategory"}
        )


# -- Dispatch --------------------------------------------------------------


class TestStatusDispatch:
    def test_fatal_category_fires_on_error_fatal_true_and_terminates(
        self, mock_pubnub
    ):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        inbound_done_hits: List[int] = []
        client.on_inbound_done(lambda: inbound_done_hits.append(1))

        status = _StatusWithIsError(
            category="PNAccessDeniedCategory",
            error=True,
            error_data={"message": "PAM revoked"},
        )

        listener.status(client._pubnub, status)

        assert len(received) == 1
        err = received[0]
        assert err.category == "PNAccessDeniedCategory"
        assert err.fatal is True
        assert err.channel == client.channel
        assert err.error == {"message": "PAM revoked"}
        assert isinstance(err.timestamp, float)

        assert client.is_active is False
        assert inbound_done_hits == [1]

        # Inbound iterator must terminate cleanly rather than hang.
        it = iter(client.inbound)
        with pytest.raises(StopIteration):
            next(it)

    def test_fatal_force_terminates_cleanly_when_bundle_end_raises(
        self, mock_pubnub,
    ):
        """Regression: on fatal PAM revocation, ``_bundle.end()`` and
        ``_bundle.publish_end_marker()`` use the dead token and raise.
        If ``end()`` lets that propagate, the teardown below (inbound
        sentinel, listener removal, pubnub.stop) never runs and the
        consumer's iterator hangs. Swap the bundle with one that raises
        on both and assert the iterator still exits cleanly.
        """
        client = StreamClient(
            subscribe_key="sub-key",
            publish_key="pub-key",
            token="revoked-token",
            agent_name="test_agent",
            stream_id="my-bidi-stream",
            direction="bidirectional",
        )
        listener = _get_registered_listener(client)

        # Replace the real bundle with one that raises on end() — the
        # exact failure mode on a revoked T7c.
        fake_bundle = MagicMock()
        fake_bundle.end.side_effect = RuntimeError("PAM denied: token revoked")
        fake_bundle.publish_end_marker.side_effect = RuntimeError(
            "PAM denied: token revoked",
        )
        client._bundle = fake_bundle

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        inbound_done_hits: List[int] = []
        client.on_inbound_done(lambda: inbound_done_hits.append(1))

        status = _StatusWithIsError(
            category="PNAccessDeniedCategory", error=True,
        )
        listener.status(client._pubnub, status)

        # onError still fires with fatal=True.
        assert len(received) == 1
        assert received[0].fatal is True

        # Despite bundle failures, teardown completed:
        assert client.is_active is False
        assert inbound_done_hits == [1]

        # Iterator exits cleanly instead of hanging.
        it = iter(client.inbound)
        with pytest.raises(StopIteration):
            next(it)

    def test_fatal_pn_bad_request_also_terminates(self, mock_pubnub):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        listener.status(
            client._pubnub,
            _StatusWithIsError(category="PNBadRequestCategory", error=True),
        )

        assert len(received) == 1
        assert received[0].fatal is True
        assert client.is_active is False

    def test_fatal_pam_denied_with_real_enum_category_force_terminates(
        self, mock_pubnub,
    ):
        # Regression: real pubnub-python delivers ``status.category`` as
        # a ``PNStatusCategory`` enum member (e.g.
        # ``PNStatusCategory.PNAccessDeniedCategory``), not a raw string.
        # The classifier + dispatcher must still force-terminate and
        # surface ``StreamError.category`` as the canonical string name.
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        status = _StatusWithEnumCategory(
            category=_FakePNStatusCategory.PNAccessDeniedCategory,
            error=True,
            error_data={"message": "PAM revoked"},
        )
        listener.status(client._pubnub, status)

        assert len(received) == 1
        err = received[0]
        # Canonical string name, NOT "" and NOT
        # "PNStatusCategory.PNAccessDeniedCategory".
        assert err.category == "PNAccessDeniedCategory"
        assert err.fatal is True
        assert client.is_active is False

    def test_benign_status_does_not_fire_on_error(self, mock_pubnub):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        listener.status(
            client._pubnub,
            _StatusWithIsError(category="PNConnectedCategory", error=False),
        )

        assert received == []
        assert client.is_active is True

    def test_non_fatal_error_fires_on_error_but_does_not_terminate(
        self, mock_pubnub
    ):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        listener.status(
            client._pubnub,
            _StatusWithIsError(category="PNNetworkIssuesCategory", error=True),
        )

        assert len(received) == 1
        assert received[0].category == "PNNetworkIssuesCategory"
        assert received[0].fatal is False
        assert client.is_active is True

    def test_consumer_callback_that_throws_does_not_break_termination(
        self, mock_pubnub, caplog
    ):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        def _raises(_err):
            raise RuntimeError("consumer boom")

        received: List[StreamError] = []
        client.on_error(_raises)
        client.on_error(lambda e: received.append(e))

        with caplog.at_level("ERROR"):
            listener.status(
                client._pubnub,
                _StatusWithIsError(
                    category="PNAccessDeniedCategory", error=True
                ),
            )

        # Second callback still fired.
        assert len(received) == 1
        assert received[0].fatal is True

        # Exception from first callback was logged with logger.exception.
        assert any(
            "on_error callback raised" in rec.message
            for rec in caplog.records
        )

        # Forced termination still happened.
        assert client.is_active is False

        # Iterator exits cleanly.
        it = iter(client.inbound)
        with pytest.raises(StopIteration):
            next(it)

    def test_multiple_on_error_callbacks_fire_in_order(self, mock_pubnub):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        order: List[str] = []
        client.on_error(lambda _e: order.append("first"))
        client.on_error(lambda _e: order.append("second"))
        client.on_error(lambda _e: order.append("third"))

        listener.status(
            client._pubnub,
            _StatusWithIsError(category="PNNetworkIssuesCategory", error=True),
        )

        assert order == ["first", "second", "third"]

    def test_status_handler_tolerates_malformed_status(self, mock_pubnub):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        # SimpleNamespace without is_error() and without matching category.
        listener.status(client._pubnub, SimpleNamespace())
        listener.status(client._pubnub, SimpleNamespace(category=None))
        listener.status(client._pubnub, SimpleNamespace(category=123))

        assert received == []
        assert client.is_active is True

    def test_stream_error_carries_channel_and_timestamp(self, mock_pubnub):
        import time as _time

        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        before = _time.time()
        listener.status(
            client._pubnub,
            _StatusWithIsError(category="PNNetworkIssuesCategory", error=True),
        )
        after = _time.time()

        assert len(received) == 1
        err = received[0]
        assert err.channel == client.channel
        assert before <= err.timestamp <= after

    def test_status_handler_is_registered_on_listener(self, mock_pubnub):
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        # Must have both status and message callbacks.
        assert hasattr(listener, "status")
        assert callable(listener.status)
        assert hasattr(listener, "message")
        assert callable(listener.message)

    def test_error_field_fallback_when_error_data_absent(self, mock_pubnub):
        """If ``error_data`` is None but ``error`` carries a truthy payload,
        the StreamError's ``error`` field falls back to that payload."""
        client = _make_inbound_client()
        listener = _get_registered_listener(client)

        received: List[StreamError] = []
        client.on_error(lambda e: received.append(e))

        listener.status(
            client._pubnub,
            _StatusWithIsError(
                category="PNNetworkIssuesCategory",
                error="network hiccup",
                error_data=None,
            ),
        )

        assert len(received) == 1
        assert received[0].error == "network hiccup"
