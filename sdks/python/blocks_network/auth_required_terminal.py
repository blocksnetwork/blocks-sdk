"""The failed-terminal fields that tell a task's caller its user must connect or approve an integration.

Parity with Node ``auth-required-terminal.ts``.
"""

import re
from typing import Any, Dict, Optional, Tuple
from urllib.parse import urlparse

from .connections import AUTH_REQUIRED_CODE, ConnectionRequiredError, parse_auth_required_details
from .rpc_client import AuthRequiredError

_DEFAULT_MESSAGE = "Integration connection required"

# A relayed refusal is copied into each caller's own terminal, so a called agent must not be able to size it.
_MAX_FIELD_LENGTH = 128
_MAX_URL_LENGTH = 2048
_MAX_MESSAGE_LENGTH = 500
# ``http(s)://host…`` with no whitespace: the same rule as the Node SDK, whatever each URL parser would repair.
_HTTP_URL = re.compile(r"^https?://[^\s/]+\S*$", re.IGNORECASE)


class RelayedAuthRequiredError(Exception):
    """A called task failed needing its user's approval. The fields are that agent's words: check ``connect_url``."""

    def __init__(self, message: str, integration: str, reason: str, connect_url: str) -> None:
        super().__init__(message)
        self.integration = integration
        self.reason = reason
        self.connect_url = connect_url


def _relayable_details(details: Any) -> Optional[Tuple[str, str, str]]:
    """The three fields as strings with an http(s) link: these travel through agents, so nothing else passes."""
    fields = parse_auth_required_details(details)
    if fields is None:
        return None
    integration, reason, connect_url = fields
    if not (0 < len(integration) <= _MAX_FIELD_LENGTH and 0 < len(reason) <= _MAX_FIELD_LENGTH):
        return None
    # fullmatch, not match: Python's ``$`` also matches before a trailing newline.
    if len(connect_url) > _MAX_URL_LENGTH or not _HTTP_URL.fullmatch(connect_url):
        return None
    try:
        parsed = urlparse(connect_url)
        _ = parsed.port  # urlparse accepts ":bad" and ":99999"; reading the port is what rejects them
    except ValueError:
        return None
    return fields if parsed.hostname else None


def _message_of(text: Any) -> str:
    return text[:_MAX_MESSAGE_LENGTH] if isinstance(text, str) and text else _DEFAULT_MESSAGE


def auth_required_terminal_fields(exc: BaseException) -> Dict[str, Any]:
    """The terminal fields for a handler error that asks the user to connect or approve, or ``{}`` for any other."""
    if not isinstance(exc, (AuthRequiredError, ConnectionRequiredError, RelayedAuthRequiredError)):
        return {}
    details = {"integration": exc.integration, "reason": exc.reason, "connectUrl": exc.connect_url}
    if _relayable_details(details) is None:
        return {}
    message = exc.rpc_message if isinstance(exc, AuthRequiredError) else str(exc)
    return {"error": _message_of(message), "errorCode": AUTH_REQUIRED_CODE, "errorDetails": details}


def auth_required_from_terminal(event: Any) -> Optional[RelayedAuthRequiredError]:
    """The relayed refusal a failed terminal carries when the task's user must connect or approve, else ``None``.

    Accepts a ``TaskEvent`` or its raw dict.
    """
    if event.get("state") != "failed" or event.get("errorCode") != AUTH_REQUIRED_CODE:
        return None
    fields = _relayable_details(event.get("errorDetails"))
    if fields is None:
        return None
    return RelayedAuthRequiredError(_message_of(event.get("error")), *fields)
