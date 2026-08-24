"""
StreamRef - Consumer-side bridge between task events and Stream SDK.

StreamRef wraps a StreamDescriptor and provides open() to create a
StreamClient via from_descriptor(). open() is idempotent while the
stream client is active and must not create duplicate clients.

When the owning ``TaskSession`` is in a terminal state at the time
``open()`` is called, the SDK short-circuits with
``StreamUnavailableError`` instead of constructing a dead client against
an already-revoked PAM token. Stream data is live-only; on reconnect
after termination, only the descriptor metadata and task artifacts
remain accessible.
"""

from __future__ import annotations

from typing import Any, Callable, Dict, Optional

from .stream import StreamClient, StreamDescriptor

# Terminal task states for which stream data is no longer accessible.
# Kept file-local to avoid a circular import from task_session (which
# imports StreamRef). Must match task_session.TERMINAL_STATES.
_TERMINAL_STATES = frozenset({"completed", "failed", "canceled"})


class StreamUnavailableError(RuntimeError):
    """Raised by :meth:`StreamRef.open` when the owning session's task is
    in a terminal state.

    Fields mirror the Node SDK's ``StreamUnavailableError`` so consumers
    can branch programmatically on the terminal state. The error message
    points at accessible alternatives (``ref.descriptor``,
    ``session.list_artifacts()``, ``session.state``).
    """

    def __init__(
        self,
        message: str,
        *,
        task_id: str,
        stream_id: str,
        declared_stream: Optional[str],
        terminal_state: str,
    ) -> None:
        super().__init__(message)
        self.task_id = task_id
        self.stream_id = stream_id
        self.declared_stream = declared_stream
        self.terminal_state = terminal_state


class StreamRef:
    """Consumer-side stream reference.

    Wraps a StreamDescriptor and provides open() to create a StreamClient
    via from_descriptor(). open() is idempotent while the stream client
    is active; once the client has been ended, further open() calls fail.

    When constructed with a ``session_state`` getter, ``open()`` checks
    the owning session's current state on every call. If the session is
    in a terminal state (``completed`` / ``failed`` / ``canceled``), the
    call raises :class:`StreamUnavailableError` before creating a
    StreamClient against an already-revoked T7c token.
    """

    def __init__(
        self,
        descriptor: StreamDescriptor,
        sdk_options: Dict[str, Any],
        on_open: Optional[Callable[[StreamClient], None]] = None,
        session_state: Optional[Callable[[], Optional[str]]] = None,
    ) -> None:
        self._descriptor = descriptor
        self._sdk_options = sdk_options
        self._client: Optional[StreamClient] = None
        self._client_ended = False
        self._on_open = on_open
        self._session_state = session_state

    @property
    def descriptor(self) -> StreamDescriptor:
        """The underlying StreamDescriptor."""
        return self._descriptor

    def open(
        self,
        *,
        reorder_timeout_ms: Optional[int] = None,
    ) -> StreamClient:
        """Open a StreamClient from this ref's descriptor.

        Resolution order (first match wins):

        1. If a StreamClient was previously opened and is still active,
           return that same client (idempotency). This also applies
           while the session is draining after a terminal event — a
           consumer that holds a live client MUST continue to receive
           it.
        2. If the previously opened client has already ended, raise a
           generic :class:`RuntimeError` ("already been ended"). The
           terminal short-circuit does NOT fire in this path, because
           "already ended" is the more specific signal for "no new
           client here".
        3. If the owning session's task is in a terminal state
           (``completed`` / ``failed`` / ``canceled``) AND no client
           has ever been constructed for this ref, raise
           :class:`StreamUnavailableError`. Stream data is live-only;
           use ``ref.descriptor``, ``session.list_artifacts()``, or
           ``session.state`` to inspect a terminal session.
        4. Otherwise, construct a new StreamClient from the descriptor.

        Uses descriptor.format for the wire format. No caller-supplied
        format override is accepted.

        Parameters
        ----------
        reorder_timeout_ms : int, optional
            Reorder buffer timeout in milliseconds. Default 750. Set to 0
            to disable reordering (yield in arrival order).
        """
        # Idempotency branches run BEFORE the terminal short-circuit: the
        # short-circuit exists to prevent *constructing* a new client against
        # a revoked T7c token, not to invalidate an already-live client. A
        # consumer that opened a stream while the task was running must be
        # able to re-call open() during the drain window (or with
        # auto_drain=False) and receive the same live client, per the
        # the idempotency rule.
        if self._client is not None and self._client.is_active:
            return self._client
        if self._client_ended:
            raise RuntimeError(
                f'StreamRef for "{self._descriptor.stream_id}" has already '
                f"been ended and cannot be reopened"
            )

        state = self._session_state() if self._session_state is not None else None
        if state in _TERMINAL_STATES and state is not None:
            stream_name = (
                self._descriptor.declared_stream or self._descriptor.stream_id
            )
            raise StreamUnavailableError(
                f'Cannot open stream "{stream_name}" on task '
                f'"{self._descriptor.task_id}": the task is in terminal '
                f'state "{state}" and stream data is live-only (not '
                f"persisted after a task ends). The stream's metadata "
                f"remains available on `ref.descriptor`; task artifacts "
                f"are available via `session.list_artifacts()`; the final "
                f"task state is in `session.state`.",
                task_id=self._descriptor.task_id,
                stream_id=self._descriptor.stream_id,
                declared_stream=self._descriptor.declared_stream,
                terminal_state=state,
            )

        open_options = dict(self._sdk_options)
        if reorder_timeout_ms is not None:
            open_options["reorder_timeout_ms"] = reorder_timeout_ms

        client = StreamClient.from_descriptor(
            self._descriptor,
            **open_options,
        )

        def _on_end() -> None:
            self._client_ended = True
            self._client = None

        client.on_end(_on_end)
        self._client = client
        if self._on_open is not None:
            self._on_open(client)
        return client

    @property
    def is_open(self) -> bool:
        """Whether this ref's stream client is currently active."""
        return self._client is not None and self._client.is_active
