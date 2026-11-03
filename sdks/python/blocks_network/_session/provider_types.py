"""What session handlers receive. Experimental: sessions are a preview, refused with
``SESSIONS_DISABLED`` until enabled for your org, and may change before general availability."""

from __future__ import annotations

import threading
from dataclasses import dataclass
from typing import Any, Callable, Dict, List, Optional, Union

from ..types import RequestPart


class SessionContext:
    """The session an ``on_session_open`` / ``on_exchange`` / ``on_session_close`` call
    belongs to. Experimental.

    ``org_id`` is ``None`` when the caller has no org.
    """

    def __init__(
        self,
        *,
        session_id: str,
        agent_name: str,
        org_id: Optional[str],
        owner_id: str,
        caller_claims: Dict[str, Any],
        close: Callable[[], None],
    ) -> None:
        self.session_id = session_id
        self.agent_name = agent_name
        self.org_id = org_id
        self.owner_id = owner_id
        self.caller_claims = caller_claims
        self._close = close

    def close(self) -> None:
        """Close the session from the provider side (``reason: "provider_closed"``)."""
        self._close()


class ExchangeContext:
    """One exchange inside a session. Experimental.

    ``report_status`` publishes ``exchange_progress`` at most once a second; a pending status
    goes out before the outcome. ``publish_artifact`` returns once the artifact is
    published. Session artifacts are inline only, so one over the inline limit raises.
    """

    def __init__(
        self,
        *,
        exchange_id: str,
        request_parts: List[RequestPart],
        cancel_event: threading.Event,
        report_status: Callable[[str], None],
        publish_artifact: Callable[..., None],
        download_input_artifact: Callable[[Any], bytes],
    ) -> None:
        self.exchange_id = exchange_id
        self.request_parts = request_parts
        self.cancel_event = cancel_event
        self._report_status = report_status
        self._publish_artifact = publish_artifact
        self._download_input_artifact = download_input_artifact

    @property
    def is_cancelled(self) -> bool:
        return self.cancel_event.is_set()

    def report_status(self, message: str) -> None:
        self._report_status(message)

    def publish_artifact(
        self,
        data: Union[bytes, str],
        *,
        mime_type: Optional[str] = None,
        file_name: Optional[str] = None,
        output_id: Optional[str] = None,
    ) -> None:
        self._publish_artifact(data, mime_type=mime_type, file_name=file_name, output_id=output_id)

    def download_input_artifact(self, part: Any) -> bytes:
        return self._download_input_artifact(part)


OnSessionOpen = Callable[[SessionContext], None]
# Returns ``None`` or a dict like a task handler's: ``{"artifacts": [{"data", "mimeType", ...}]}``.
OnExchange = Callable[[SessionContext, ExchangeContext], Optional[Dict[str, Any]]]
OnSessionClose = Callable[[SessionContext, str], None]


@dataclass(frozen=True)
class SessionHandlers:
    on_exchange: OnExchange
    on_session_open: Optional[OnSessionOpen] = None
    on_session_close: Optional[OnSessionClose] = None
