"""The route's PubNub client, narrowed to what the provider runtime uses."""

from __future__ import annotations

from typing import Any, Callable, Dict, Optional, Protocol

from pubnub.callbacks import SubscribeCallback

from ..artifacts import download_artifact
from ..stream.stream_client import _map_transport_category
from ..types import ArtifactRef

OnMessage = Callable[[Optional[str], Optional[str], Any], None]
# Receives a neutral category: "connected", "reconnected", "access_denied", ...
OnStatus = Callable[[str], None]


class RouteClient(Protocol):
    def set_token(self, token: str) -> None: ...

    def listen(self, on_message: OnMessage, on_status: OnStatus) -> None: ...

    def subscribe(self, channel: str) -> None: ...

    def publish(self, channel: str, message: Dict[str, Any], meta: Dict[str, Any]) -> None:
        """Return once delivered; raise on failure."""
        ...

    def download_artifact(self, ref: Any) -> bytes: ...

    def close(self, channel: str) -> None: ...


class PubNubRouteClient:
    def __init__(self, pubnub: Any) -> None:
        self._pubnub = pubnub
        self._listener: Any = None

    def set_token(self, token: str) -> None:
        self._pubnub.set_token(token)

    def listen(self, on_message: OnMessage, on_status: OnStatus) -> None:
        class _Listener(SubscribeCallback):
            def message(self, _pubnub: Any, event: Any) -> None:
                on_message(
                    getattr(event, "channel", None),
                    getattr(event, "publisher", None),
                    getattr(event, "message", None),
                )

            def status(self, _pubnub: Any, status: Any) -> None:
                on_status(_map_transport_category(getattr(status, "category", None)))

            def presence(self, _pubnub: Any, _event: Any) -> None:
                pass

        # Kept only once added: a close racing this then removes a listener that is installed.
        listener = _Listener()
        self._pubnub.add_listener(listener)
        self._listener = listener

    def subscribe(self, channel: str) -> None:
        self._pubnub.subscribe().channels([channel]).execute()

    def publish(self, channel: str, message: Dict[str, Any], meta: Dict[str, Any]) -> None:
        self._pubnub.publish().channel(channel).message(message).meta(meta).should_store(
            True
        ).use_post(True).sync()

    def download_artifact(self, ref: Any) -> bytes:
        artifact_ref = ref if isinstance(ref, ArtifactRef) else ArtifactRef.from_dict(ref)
        return download_artifact(artifact_ref, self._pubnub).data

    def close(self, channel: str) -> None:
        """Safe to call again: each step runs even if an earlier one raises."""
        listener, self._listener = self._listener, None
        steps = [
            lambda: self._pubnub.unsubscribe().channels([channel]).execute(),
            self._pubnub.stop,
        ]
        if listener is not None:
            steps.insert(0, lambda: self._pubnub.remove_listener(listener))
        errors = []
        for step in steps:
            try:
                step()
            except Exception as exc:
                errors.append(exc)
        if errors:
            raise errors[0]
