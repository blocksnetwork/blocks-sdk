"""Consumer-writer split test for ``shared_up``
(shared-stream lifecycle).

On ``shared_up`` (``affinity: 'shared'``, ``agentDirection: 'inbound'``),
consumer direction inverts to ``outbound`` — the consumer builds a
``StreamClient`` via ``from_descriptor`` that is the WRITER on the
shared channel. The agent is the reader.

Contract: consumer-writer ``StreamClient.end()`` MUST NOT publish
``stream_end`` on a shared channel. The affinity gate sits inside
``StreamClient.end()`` so both the producer-side StreamClient and the
consumer-side StreamClient built via ``from_descriptor`` inherit the
same rule — fix (c) and fix (f) collapsed into a single gate.

Mirrors ``blocks-sdk/sdks/node/tests/shared-up-consumer-writer.test.ts``.
"""

from __future__ import annotations

from typing import Any, Dict, List
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.stream.descriptor import StreamDescriptor
from blocks_network.stream.stream_client import StreamClient, _reset_uuid_counter


# ---------------------------------------------------------------------------
# PubNub mock — tracks every publish so we can assert on stream_end.
# ---------------------------------------------------------------------------


@pytest.fixture(autouse=True)
def _reset_counter() -> None:
    _reset_uuid_counter()
    yield
    _reset_uuid_counter()


def _make_mock_pubnub_and_publishes() -> tuple[MagicMock, List[Dict[str, Any]]]:
    """Build a PubNub mock with a capturing publish builder chain.

    Returns the pubnub instance plus a list the test can inspect for
    every publish message ever sent through the chain.
    """
    published: List[Dict[str, Any]] = []
    instance = MagicMock()
    instance.set_token = MagicMock()

    def _make_chain() -> MagicMock:
        chain = MagicMock()
        record: Dict[str, Any] = {}

        def _channel(ch: str) -> MagicMock:
            record["channel"] = ch
            return chain

        def _message(msg: Any) -> MagicMock:
            record["message"] = msg
            return chain

        def _meta(m: Any) -> MagicMock:
            return chain

        def _should_store(v: Any) -> MagicMock:
            return chain

        def _use_post(v: Any) -> MagicMock:
            return chain

        def _sync() -> MagicMock:
            if isinstance(record.get("message"), dict):
                published.append({
                    "channel": record.get("channel", ""),
                    "message": dict(record["message"]),
                })
            return MagicMock()

        chain.channel = _channel
        chain.message = _message
        chain.meta = _meta
        chain.should_store = _should_store
        chain.use_post = _use_post
        chain.sync = _sync
        return chain

    instance.publish.side_effect = lambda: _make_chain()
    instance.subscribe.return_value = _make_chain()
    instance.unsubscribe.return_value = _make_chain()

    # here_now builder chain
    here_now_result = MagicMock()
    here_now_result.result.channels = []
    here_now_sync = MagicMock(return_value=here_now_result)
    here_now_channels = MagicMock(return_value=MagicMock(sync=here_now_sync))
    instance.here_now.return_value = MagicMock(channels=here_now_channels)

    instance.add_listener = MagicMock()
    instance.remove_listener = MagicMock()
    instance.unsubscribe_all = MagicMock()
    instance.stop = MagicMock()
    return instance, published


class _FakeConfig:
    def __init__(self) -> None:
        self.subscribe_key = None
        self.publish_key = None
        self.user_id = None


@pytest.fixture
def pubnub_mock():
    """Yields (pn_class_mock, captured_published_per_instance)."""
    instances_and_publishes: List[tuple[MagicMock, List[Dict[str, Any]]]] = []

    def _pn_factory(_config: Any) -> MagicMock:
        inst, published = _make_mock_pubnub_and_publishes()
        instances_and_publishes.append((inst, published))
        return inst

    fake_config = _FakeConfig()
    with patch("blocks_network.stream.stream_client.PubNub") as mock_cls, \
         patch(
             "blocks_network.stream.stream_client.PNConfiguration",
             return_value=fake_config,
         ):
        mock_cls.side_effect = _pn_factory
        yield instances_and_publishes


def _make_consumer_writer_descriptor(
    *,
    task_id: str = "task-c1",
    token: str = "T7c-c1",
    affinity: str = "shared",
    stream_id: str = "shared_up",
) -> StreamDescriptor:
    return StreamDescriptor(
        task_id=task_id,
        stream_id=stream_id,
        agent_name="sharedup_test",
        channel=f"stream.sharedup_test.{stream_id}",
        token=token,
        agent_direction="inbound",
        local_direction="outbound",
        format="events",
        affinity=affinity,
        declared_stream=stream_id,
    )


def _end_markers(published: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    return [
        p for p in published
        if isinstance(p.get("message"), dict)
        and p["message"].get("type") == "stream_end"
    ]


class TestSharedUpConsumerWriter:
    """Two consumer-writers on shared_up end() without publishing
    stream_end."""

    def test_two_consumer_writers_end_no_marker(self, pubnub_mock) -> None:
        desc_a = _make_consumer_writer_descriptor(task_id="task-c1", token="T7c-c1")
        desc_b = _make_consumer_writer_descriptor(task_id="task-c2", token="T7c-c2")

        client1 = StreamClient.from_descriptor(
            desc_a, subscribe_key="sub-key", publish_key="pub-key",
        )
        client2 = StreamClient.from_descriptor(
            desc_b, subscribe_key="sub-key", publish_key="pub-key",
        )

        assert client1.is_active is True
        assert client2.is_active is True

        client1.write({"from": "c1", "seq": 1})
        client2.write({"from": "c2", "seq": 1})

        client1.end()
        client2.end()

        # Merge published lists from both underlying PubNub instances.
        merged_published: List[Dict[str, Any]] = []
        for _inst, published in pubnub_mock:
            merged_published.extend(published)

        # Core assertion: NO stream_end marker was published to the
        # shared channel from either consumer-writer's cleanup path.
        assert _end_markers(merged_published) == []

        assert client1.is_active is False
        assert client2.is_active is False

    def test_dedicated_consumer_writer_still_publishes_marker(
        self, pubnub_mock,
    ) -> None:
        """Regression gate: the affinity gate is specific to ``shared``.
        A ``dedicated`` consumer-writer MUST still publish ``stream_end``
        — over-broad suppression would regress the dedicated-stream
        contract (marker emission on per-task cleanup).
        """
        desc = _make_consumer_writer_descriptor(
            task_id="task-c3",
            token="T7c-c3",
            affinity="dedicated",
            stream_id="ded_up",
        )

        client = StreamClient.from_descriptor(
            desc, subscribe_key="sub-key", publish_key="pub-key",
        )
        client.write({"seq": 1})
        client.end()

        merged_published: List[Dict[str, Any]] = []
        for _inst, published in pubnub_mock:
            merged_published.extend(published)

        assert len(_end_markers(merged_published)) == 1
