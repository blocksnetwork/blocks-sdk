"""Node mirror: tests/stream-wire-fixtures.test.ts. Each SDK owns the fixture its StreamClient
publishes and decodes every SDK's fixture, so a wire change on one side fails the other.
Regenerate this SDK's fixture with UPDATE_STREAM_FIXTURES=1.
"""

from __future__ import annotations

import json
import os
import threading
from concurrent.futures import ThreadPoolExecutor
from concurrent.futures import TimeoutError as FutureTimeout
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Callable, Dict, Iterable, List
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.stream.stream_client import StreamClient

FIXTURES_DIR = Path(__file__).resolve().parents[3] / "fixtures" / "streams"
PRODUCERS = ("node", "python")
OWN_PRODUCER = "python"
FIXED_NOW_MS = 1_700_000_000_000
DECODE_TIMEOUT_S = 5


def _deterministic_bytes(size: int, seed: int) -> bytes:
    """Deterministic LCG; the Node mirror generates the same bytes from the same seed."""
    out = bytearray(size)
    s = seed & 0xFFFFFFFF
    for i in range(size):
        s = ((s * 1103515245 + 12345) & 0xFFFFFFFF) & 0x7FFFFFFF
        out[i] = s & 0xFF
    return bytes(out)


# A 16384-byte write exceeds the 16384 max message size once encoded, so it splits into multipart.
BYTES_WRITES: List[bytes] = [
    b"",
    bytes(1024),
    b"\xff" * 1024,
    b"\x42" * 16384,
    _deterministic_bytes(4096, 0xBEEF),
]

EVENT_WRITES: List[Any] = [
    {"type": "primitive", "n": 42, "b": True, "s": "hello", "nullValue": None},
    {"type": "nested", "meta": {"tags": ["a", "b", "c"], "count": 7, "deep": {"x": {"y": "z"}}}},
    {"type": "special", "text": "emoji \U0001F680 RTL מבחן"},
    *({"type": "batch", "i": i} for i in range(10)),
]


class _FakePubNub:
    def __init__(self) -> None:
        self.published: List[Dict[str, Any]] = []
        self.listeners: List[Any] = []
        self._lock = threading.Lock()

    def publish(self) -> Any:
        fake = self
        chain = MagicMock()
        for name in ("channel", "meta", "should_store", "use_post"):
            getattr(chain, name).return_value = chain

        def _message(message: Dict[str, Any]) -> Any:
            with fake._lock:
                fake.published.append(message)
            return chain

        chain.message.side_effect = _message
        return chain

    def add_listener(self, listener: Any) -> None:
        self.listeners.append(listener)

    def __getattr__(self, name: str) -> Any:
        return MagicMock()


@pytest.fixture()
def fake_pubnub():
    fake = _FakePubNub()
    with patch("blocks_network.stream.stream_client.PubNub", return_value=fake), patch(
        "blocks_network.stream.stream_bundle._now_ms", return_value=FIXED_NOW_MS
    ):
        yield fake


def _make_client(fmt: str, direction: str) -> StreamClient:
    return StreamClient(
        subscribe_key="sub-key",
        publish_key="pub-key",
        token="token",
        agent_name="fixture_agent",
        stream_id=f"fixture-{fmt}",
        format=fmt,
        direction=direction,
        max_message_size=16384,
        bundle_size_bytes=4096,
        max_latency_ms=60_000,
        gating=False,
    )


def _wire_order(message: Dict[str, Any]) -> tuple:
    """Multipart parts publish concurrently, so order the capture for a stable fixture."""
    part = (message.get("multipart") or {}).get("part", 0)
    return (message["type"] == "stream_end", message["seq"], part)


def _produce(fake: _FakePubNub, fmt: str, writes: List[Any]) -> List[Dict[str, Any]]:
    fake.published.clear()
    client = _make_client(fmt, "outbound")
    for write in writes:
        client.write(write)
    client.end()
    return sorted(fake.published, key=_wire_order)


def _feed(fake: _FakePubNub, client: StreamClient, messages: List[Dict[str, Any]]) -> None:
    listener = fake.listeners[-1]
    for message in messages:
        listener.message(fake, SimpleNamespace(channel=client.channel, message=message))


def _drain(client: StreamClient, read: Callable[[], Iterable[Any]]) -> List[Any]:
    """Read the inbound iterator off the test thread: an unreadable stream_end fails
    after a deadline instead of hanging, and a decoder error still fails the test."""
    pool = ThreadPoolExecutor(max_workers=1)
    future = pool.submit(lambda: list(read()))
    try:
        return future.result(timeout=DECODE_TIMEOUT_S)
    except FutureTimeout:
        client.end()
        pytest.fail(f"stream did not end within {DECODE_TIMEOUT_S}s")
    finally:
        pool.shutdown(wait=False)


def _fixture_path(producer: str) -> Path:
    return FIXTURES_DIR / f"{producer}-produced.json"


def _read_fixture(producer: str) -> Dict[str, List[Dict[str, Any]]]:
    return json.loads(_fixture_path(producer).read_text(encoding="utf-8"))


def test_own_fixture_matches_what_this_sdk_publishes(fake_pubnub: _FakePubNub) -> None:
    actual = {
        "bytes": _produce(fake_pubnub, "bytes", BYTES_WRITES),
        "events": _produce(fake_pubnub, "events", EVENT_WRITES),
    }
    if os.environ.get("UPDATE_STREAM_FIXTURES") == "1":
        _fixture_path(OWN_PRODUCER).write_text(
            json.dumps(actual, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
        )
    assert actual == _read_fixture(OWN_PRODUCER), "stale fixture: rerun with UPDATE_STREAM_FIXTURES=1"


@pytest.mark.parametrize("producer", PRODUCERS)
def test_decodes_bytes_to_the_bytes_written(fake_pubnub: _FakePubNub, producer: str) -> None:
    client = _make_client("bytes", "inbound")
    _feed(fake_pubnub, client, _read_fixture(producer)["bytes"])
    assert b"".join(_drain(client, client.bytes)) == b"".join(BYTES_WRITES)


@pytest.mark.parametrize("producer", PRODUCERS)
def test_decodes_events_in_order(fake_pubnub: _FakePubNub, producer: str) -> None:
    client = _make_client("events", "inbound")
    _feed(fake_pubnub, client, _read_fixture(producer)["events"])
    assert _drain(client, client.events) == EVENT_WRITES
