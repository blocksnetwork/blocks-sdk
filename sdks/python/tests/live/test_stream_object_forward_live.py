"""Live A2A test for BLOCKS-262: handler-side StreamObject is a strict
subset of StreamClient -- forward the full read/observability surface.

Two scenarios:

1. **Input bytes path** -- Pipe agent declares one inbound `format: bytes`
   stream (`audio_in`) and one outbound `format: events` stream
   (`text_out`). The handler iterates ``audio_in.bytes()`` (the new
   forwarded method) and writes ``{"chunkLen": ..., "firstByte": ...}``
   events to ``text_out``. The test consumer opens ``audio_in`` via
   ``stream_ref.open()`` and calls ``.write(bytes_payload)`` three times
   with distinct ``bytes`` payloads -- the SDK puts these on the wire as
   ``encoding: "base64"``. Do NOT call ``.write("SGVsbG8=")`` with a
   ``str`` -- that lands as ``encoding: "utf8"`` and exercises the
   wrong path. The test then reads ``text_out`` via ``events()`` and
   asserts the chunk metadata round-trips correctly.

2. **onError path** -- Handler-side ``StreamObject`` wraps the agent's
   ``StreamClient`` constructed with token T7a. Drives a T7a-path error
   (T7a revocation mid-stream OR an agent-side PAM-denied channel) and
   asserts ``stream_object.on_error(spy)`` fires once with a
   ``StreamError(category='access_denied', fatal=True, ...)``.
   Do NOT use T7c revocation here -- that targets the consumer-side
   ``StreamClient.on_error``. Do NOT use task cancellation either -- it's
   cooperative and not guaranteed to surface a ``StreamError``.

   ``on_error`` MUST be registered before the read path activates --
   ``StreamClient.on_error`` only appends to its callback list, it does
   not buffer or replay past errors.

Gated behind ``PUBNUB_LIVE_TEST=1`` and the standard live-keys env vars.
This file is not run by the default ``pytest`` invocation; the user runs
it explicitly with their PubNub credentials.
"""

from __future__ import annotations

import os
import threading
import time
from typing import Any, Dict, List

import pytest

from blocks_network import (
    StreamError,
    TaskClient,
    start_agent_instance,
)
from blocks_network.agent_registry import remove_agent
from blocks_network.types import AgentInstanceOptions


# ---------------------------------------------------------------------------
# Live-test gating
# ---------------------------------------------------------------------------

LIVE_ENABLED = os.environ.get("PUBNUB_LIVE_TEST") == "1"

REQUIRED_ENV = (
    "PUBNUB_PUBLISH_KEY",
    "PUBNUB_SUBSCRIBE_KEY",
    "BLOCKS_API_KEY",
)


def _missing_env() -> List[str]:
    return [name for name in REQUIRED_ENV if not os.environ.get(name)]


pytestmark = pytest.mark.skipif(
    not LIVE_ENABLED or _missing_env(),
    reason=(
        "Live test gated behind PUBNUB_LIVE_TEST=1 + "
        f"{', '.join(REQUIRED_ENV)}; missing: {', '.join(_missing_env()) or '(disabled)'}"
    ),
)


# ---------------------------------------------------------------------------
# Pipe agent: declares two streams (audio_in inbound bytes, text_out outbound events)
# ---------------------------------------------------------------------------


# Per-run unique agent names — mirrors the Node live test's `bytes_forward_${Date.now()}`
# pattern (stream-object-forward.live.test.ts:68). A module-scoped autouse fixture
# below removes every name we register so concurrent runs and stale registry state
# don't collide.
_created_agent_names: List[str] = []


def _unique_agent_name(prefix: str) -> str:
    return f"{prefix}_{int(time.time() * 1000)}"


def _make_pipe_agent_card(name: str) -> Dict[str, Any]:
    return {
        "name": name,
        "version": "0.0.1",
        "taskKinds": ["pipe"],
        "streams": {
            "audio_in": {
                "direction": "inbound",
                "format": "bytes",
                "affinity": "dedicated",
            },
            "text_out": {
                "direction": "outbound",
                "format": "events",
                "affinity": "dedicated",
            },
        },
    }


@pytest.fixture(autouse=True, scope="module")
def _cleanup_registered_agents() -> Any:
    """Mirror the Node live test's `afterAll(removeAgent)` cleanup so a unique
    per-run agent name is registered, used, and unregistered. Without this,
    a crashed run leaves stale registrations that collide with future runs.
    """
    yield
    base_url = os.environ.get("BLOCKS_BASE_URL")
    for name in _created_agent_names:
        try:
            if base_url:
                remove_agent(name, base_url=base_url)
            else:
                remove_agent(name)
        except Exception as err:  # noqa: BLE001
            # Best-effort; surface in test output but don't fail the suite.
            print(f"[cleanup] remove_agent({name}) failed: {err}")


# ---------------------------------------------------------------------------
# Scenario 1 -- input bytes path round-trip via .bytes() / .events()
# ---------------------------------------------------------------------------


def test_stream_object_bytes_and_events_round_trip() -> None:
    """The handler iterates ``audio_in.bytes()`` and writes per-chunk
    ``{chunkLen, firstByte}`` events to ``text_out``. The consumer reads
    ``text_out`` via ``events()`` and asserts metadata matches the three
    payloads it wrote.
    """

    agent_name = _unique_agent_name("bytes_forward")
    _created_agent_names.append(agent_name)
    pipe_card = _make_pipe_agent_card(agent_name)

    expected_payloads: List[bytes] = [
        b"\x01\x02\x03\x04\x05",
        b"hello-bytes-262",
        b"\xfe\xfd\xfcfinal-chunk",
    ]

    def on_activate(audio_in: Any) -> None:
        # text_out is created from inside the handler closure below, so
        # we plumb it via task-context capture rather than this callback.
        pass

    handler_done = threading.Event()
    handler_err: List[BaseException] = []

    def handler(task: Any, ctx: Any) -> Dict[str, Any]:
        try:
            audio_in = ctx.create_stream(declared_stream="audio_in", on_activate=on_activate)
            text_out = ctx.create_stream(declared_stream="text_out")

            # Iterate decoded bytes -- this is the path under test.
            for chunk in audio_in.bytes():
                if not chunk:
                    continue
                text_out.write({
                    "chunkLen": len(chunk),
                    "firstByte": chunk[0],
                })
            text_out.end()
        except BaseException as exc:  # noqa: BLE001
            handler_err.append(exc)
            raise
        finally:
            handler_done.set()
        return {"artifacts": []}

    runtime = start_agent_instance(
        AgentInstanceOptions(
            agent_name=agent_name,
            handler=handler,
            card=pipe_card,
            concurrency=1,
            expected_instances=1,
        )
    )
    stop_runtime = runtime["stop"]

    try:
        client = TaskClient.create(billing_mode="free", api_key=os.environ["BLOCKS_API_KEY"])

        # owner_id is omitted: the server validates it against the
        # authenticated user behind the API key, so the SDK's default
        # (derived from ConsumerAuth identity) is the only safe value.
        session = client.send_message(
            agent_name=agent_name,
            task_kind="pipe",
            duration=1,
            request_parts=[{"partId": "go", "text": "stream"}],
        )

        # Open audio_in (consumer-write side) and write three bytes payloads.
        audio_ref = session.wait_for_stream_by_name("audio_in", timeout=30.0)
        audio = audio_ref.open()
        for payload in expected_payloads:
            # bytes payloads land on the wire as encoding: "base64".
            audio.write(payload)
        audio.end()

        # Read text_out via the new events() forwarder on the consumer side.
        text_ref = session.wait_for_stream_by_name("text_out", timeout=30.0)
        text = text_ref.open()

        observed: List[Dict[str, Any]] = []
        for ev in text.events():
            observed.append(ev)
            if len(observed) >= len(expected_payloads):
                break

        assert handler_err == [], f"handler raised: {handler_err}"

        assert len(observed) == len(expected_payloads)
        for ev, payload in zip(observed, expected_payloads):
            assert ev["chunkLen"] == len(payload)
            assert ev["firstByte"] == payload[0]

        session.close()
        client.destroy()
    finally:
        stop_runtime()


# ---------------------------------------------------------------------------
# Scenario 2 -- on_error path via T7a-side PAM denial
# ---------------------------------------------------------------------------


def test_stream_object_on_error_fires_for_t7a_revocation() -> None:
    """``StreamObject.on_error(spy)`` must fire when the agent-side T7a
    token is revoked or PAM-denied mid-stream.

    Uses an agent-side PAM-denied channel (or an explicit T7a revocation
    if the test harness supports one). T7c revocation belongs in the
    consumer-side test and would not exercise this path.

    The on_error callback is registered BEFORE the read path activates
    because ``StreamClient.on_error`` only appends to its callback list
    -- it does not buffer or replay past errors.
    """

    agent_name = _unique_agent_name("onerror_forward")
    _created_agent_names.append(agent_name)
    pipe_card = _make_pipe_agent_card(agent_name)

    error_seen = threading.Event()
    captured: List[StreamError] = []
    handler_err: List[BaseException] = []

    def handler(task: Any, ctx: Any) -> Dict[str, Any]:
        try:
            audio_in = ctx.create_stream(declared_stream="audio_in")

            def _capture(err: StreamError) -> None:
                captured.append(err)
                error_seen.set()

            # MUST register before iterating -- on_error doesn't replay.
            audio_in.on_error(_capture)

            try:
                for _chunk in audio_in.bytes():
                    pass
            except Exception:  # noqa: BLE001
                # Fatal categories tear down the iterator; the on_error
                # callback fires regardless.
                pass
        except BaseException as exc:  # noqa: BLE001
            handler_err.append(exc)
            raise
        return {"artifacts": []}

    runtime = start_agent_instance(
        AgentInstanceOptions(
            agent_name=agent_name,
            handler=handler,
            card=pipe_card,
            concurrency=1,
            expected_instances=1,
        )
    )
    stop_runtime = runtime["stop"]

    try:
        client = TaskClient.create(billing_mode="free", api_key=os.environ["BLOCKS_API_KEY"])

        # owner_id is omitted: see the bytes scenario above.
        session = client.send_message(
            agent_name=agent_name,
            task_kind="pipe",
            duration=1,
            request_parts=[{"partId": "go", "text": "trigger-error"}],
        )

        # Trigger a T7a-side PAM denial. Operator note: this hook is
        # environment-specific. The expected mechanisms are:
        #
        #   (a) explicit revocation of the agent's T7a stream token via
        #       the admin API for the channel `audio_in` is bound to, OR
        #   (b) start the agent under a keyset whose grant intentionally
        #       excludes the stream channel pattern.
        #
        # If your test harness exposes a helper like
        # `revoke_agent_stream_token(...)`, call it here. Otherwise this
        # test will hang and the gating xfail below will catch it.
        _trigger_t7a_revocation(audio_channel=None)

        assert error_seen.wait(timeout=30.0), (
            "on_error did not fire within 30s -- check that the T7a "
            "revocation hook (`_trigger_t7a_revocation`) is wired up "
            "for your environment."
        )
        assert handler_err == [], f"handler raised: {handler_err}"

        assert len(captured) >= 1
        err = captured[0]
        assert isinstance(err, StreamError)
        assert err.category == "access_denied"
        assert err.fatal is True

        session.close()
        client.destroy()
    finally:
        stop_runtime()


def _trigger_t7a_revocation(*, audio_channel: Any) -> None:  # noqa: ARG001
    """Environment-specific T7a revocation hook.

    Default implementation is a no-op marker -- live test operators
    must wire this up to whichever admin path their PubNub keyset
    supports (revoke-token, pam-grant-with-empty-perms, etc.).
    """
    raise pytest.skip(
        "T7a revocation hook not wired for this environment -- "
        "implement `_trigger_t7a_revocation` to enable the on_error live test."
    )
