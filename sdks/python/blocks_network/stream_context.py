"""
Stream Processing Context

Wraps a StreamClient from the Stream SDK and manages the on_activate
callback lifecycle. Each embedded stream ID maps to one processing
context. The context owns the StreamClient instance and provides the
stream object interface to on_activate callbacks.

Python-specific: on_activate runs on a dedicated daemon thread managed
by the stream context. The thread is started when the stream is
activated and stopped when the stream is destroyed.
"""

from __future__ import annotations

import logging
import threading
from typing import Any, Callable, Iterator, List, Optional

logger = logging.getLogger(__name__)

# Type alias for the on_activate callback signature
OnActivateCallback = Callable[["StreamObject"], None]
FailStreamCallback = Callable[[str, str], None]


class StreamObject:
    """The stream object passed to on_activate and returned from create_stream.

    Wraps a StreamClient from the Phase 2 Stream SDK and provides the
    developer-facing interface for stream I/O.

    ``end()`` delegates through ``release_stream`` (an ``AgentInstance``
    hook) so shared-affinity streams can be released task-scoped without
    tearing down the cross-task writer until the last ref-holder
    releases. See fix (d).
    """

    def __init__(
        self,
        stream_id: str,
        client: Any,
        *,
        external: bool = False,
        task_id: Optional[str] = None,
        release_stream: Optional[Callable[[str, str], None]] = None,
    ) -> None:
        self._stream_id = stream_id
        self._client = client
        self._external = external
        self._task_id = task_id
        self._release_stream = release_stream

    @property
    def stream_id(self) -> str:
        return self._stream_id

    @property
    def channel(self) -> str:
        return self._client.channel

    @property
    def is_active(self) -> bool:
        return self._client.is_active

    @property
    def external(self) -> bool:
        return self._external

    def write(self, data: Any) -> None:
        """Write data to the stream. Delegates to StreamClient.write()."""
        self._client.write(data)

    def end(self) -> None:
        """End the stream.

        When bound to an ``AgentInstance`` release hook (and a task id),
        delegates to the task-scoped release path:
        - Evicts the per-task shared-stream handle cache entry.
        - Decrements the registry's task tracking for the stream.
        - Shared streams: underlying ``StreamClient`` is kept alive
          until the last ref-holder releases, and no ``stream_end``
          marker is ever published on per-task cleanup.
        - Dedicated streams: the final release tears down the
          ``StreamClient`` which still publishes the end marker.

        Falls back to a direct ``StreamClient.end()`` (legacy / tests
        that construct a StreamObject without a hook).
        """
        if self._release_stream is not None and self._task_id is not None:
            self._release_stream(self._stream_id, self._task_id)
            return
        self._client.end()

    @property
    def inbound(self) -> Iterator:
        """Inbound message iterator. Delegates to StreamClient.inbound.

        Low-level wire iterator. For most read paths, prefer the decoded
        helpers ``bytes()`` (for ``format: bytes``) or ``events()`` (for
        ``format: events``).
        """
        return self._client.inbound

    @property
    def uuid(self) -> str:
        """Underlying StreamClient uuid for log correlation."""
        return self._client.uuid

    def bytes(self) -> Iterator[bytes]:
        """Decoded byte iterator. Delegates to StreamClient.bytes()."""
        return self._client.bytes()

    def events(self) -> Iterator[Any]:
        """Flattened event iterator. Delegates to StreamClient.events()."""
        return self._client.events()

    def as_file(self) -> Any:
        """File-like wrapper for ``shutil.copyfileobj`` / subprocess pipe.

        Delegates to ``StreamClient.as_file()``.
        """
        return self._client.as_file()

    def on_end(self, cb: Callable[[], None]) -> None:
        """Register a callback to fire when the stream ends."""
        self._client.on_end(cb)

    def on_error(self, cb: Callable[[Any], None]) -> None:
        """Register a callback to fire on stream-level errors.

        Delegates to ``StreamClient.on_error()``. The callback receives a
        :class:`StreamError` payload with the PubNub status category, the
        affected channel, a timestamp, and a ``fatal`` flag.

        Note: ``StreamClient.on_error`` only appends to its callback list;
        it does not buffer or replay past errors. Register the callback
        before the read path activates.
        """
        self._client.on_error(cb)


class ExternalStreamObject:
    """Stream object for external streams (no StreamClient).

    write() and inbound throw; token and activate are available.
    """

    def __init__(
        self,
        stream_id: str,
        channel: str,
        t7a_token: str,
        activate_fn: Callable,
    ) -> None:
        self._stream_id = stream_id
        self._channel = channel
        self._t7a_token = t7a_token
        self._activate_fn = activate_fn
        self._ended = False
        self._end_callbacks: List[Callable[[], None]] = []

    @property
    def stream_id(self) -> str:
        return self._stream_id

    @property
    def channel(self) -> str:
        return self._channel

    @property
    def is_active(self) -> bool:
        return not self._ended

    @property
    def external(self) -> bool:
        return True

    def write(self, data: Any) -> None:
        raise RuntimeError(
            "Cannot write to an external stream -- use Stream SDK directly"
        )

    def end(self) -> None:
        self._ended = True
        for cb in self._end_callbacks:
            cb()
        self._end_callbacks.clear()

    @property
    def inbound(self) -> Iterator:
        raise RuntimeError("Cannot read from an external stream")

    @property
    def uuid(self) -> str:
        raise RuntimeError("Cannot read uuid on an external stream")

    def bytes(self) -> Iterator[bytes]:
        raise RuntimeError("Cannot read from an external stream")

    def events(self) -> Iterator[Any]:
        raise RuntimeError("Cannot read from an external stream")

    def as_file(self) -> Any:
        raise RuntimeError("Cannot read from an external stream")

    def on_end(self, cb: Callable[[], None]) -> None:
        self._end_callbacks.append(cb)

    def on_error(self, cb: Callable[[Any], None]) -> None:
        raise RuntimeError("Cannot subscribe to errors on an external stream")

    @property
    def token(self) -> str:
        """T7a token from Phase 1 token_request handshake."""
        return self._t7a_token

    def activate(self, *, metadata: Optional[dict] = None) -> None:
        """Trigger Phase 2 activate handshake."""
        self._activate_fn(metadata=metadata)


def run_on_activate(
    stream_id: str,
    stream_object: Any,
    callback: OnActivateCallback,
    fail_stream_cb: FailStreamCallback,
) -> threading.Thread:
    """Run the on_activate callback on a dedicated daemon thread.

    Returns the thread object for tracking. If the callback throws,
    the error is caught and fail_stream is called automatically.
    """

    def _worker() -> None:
        try:
            callback(stream_object)
        except Exception as exc:
            err_msg = str(exc) if str(exc) else "stream_crashed"
            logger.error(
                "[StreamContext] on_activate error for stream %r: %s",
                stream_id,
                err_msg,
            )
            try:
                fail_stream_cb(stream_id, "stream_crashed")
            except Exception as fail_exc:
                logger.error(
                    "[StreamContext] fail_stream error for %r: %s",
                    stream_id,
                    fail_exc,
                )

    thread = threading.Thread(target=_worker, daemon=True)
    thread.start()
    return thread
