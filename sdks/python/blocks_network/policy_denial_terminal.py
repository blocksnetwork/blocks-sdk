"""The failed-terminal fields that tell a task's caller the policy gate stopped it.

Parity with Node ``policy-denial-terminal.ts``.
"""

from dataclasses import dataclass
from typing import Any, Optional, Tuple

_POLICY_REJECTED_CODE = "policy_rejected"
_POLICY_UNAVAILABLE_CODE = "policy_unavailable"
_STAGES = ("input", "response")

# A denial is read off a terminal an agent may have written, so every detail is bounded.
_MAX_REASON_LENGTH = 200
_MAX_POLICY_IDS = 16
_MAX_POLICY_ID_LENGTH = 64


@dataclass(frozen=True)
class PolicyDenial:
    """Why the policy gate stopped a task: ``policy_rejected`` when a policy said no, ``policy_unavailable`` when the check broke.

    ``stage`` is ``input`` (the task, before the agent got it) or ``response`` (an answer, before the caller got it).
    ``reason`` is never set on ``policy_unavailable``, and is ``None`` when the gate gave none. ``policy_ids`` names the
    policy versions in force for the check, and is empty when the gate lost the check.

    .. warning:: Experimental: the policy gate is a preview and this shape may change before general availability.
    """

    code: str
    stage: str
    policy_ids: Tuple[str, ...]
    reason: Optional[str] = None


def _is_short_text(value: Any, maximum: int) -> bool:
    return isinstance(value, str) and 0 < len(value) <= maximum


def policy_ids_of(value: Any) -> Optional[Tuple[str, ...]]:
    """``value`` as a tuple of policy ids, or ``None`` unless it is a short list of short strings."""
    if not isinstance(value, list) or len(value) > _MAX_POLICY_IDS:
        return None
    if not all(_is_short_text(policy_id, _MAX_POLICY_ID_LENGTH) for policy_id in value):
        return None
    return tuple(value)


def policy_denial_from_terminal(event: Any) -> Optional[PolicyDenial]:
    """The policy denial in a failed terminal's ``error`` and ``errorDetails``, else ``None`` (or on malformed details).

    Accepts a ``TaskEvent`` or its raw dict. Unlike ``wait_for_terminal``'s auth refusal, this never raises:
    ``wait_for_terminal`` still returns on a denial. A denial is trustworthy only where the policy gate's response
    check is deployed, since it strips ``errorDetails`` from agent terminals that carry a policy code.

    .. warning:: Experimental: the policy gate is a preview and this may change before general availability.
    """
    code = event.get("error")
    if event.get("state") != "failed" or code not in (_POLICY_REJECTED_CODE, _POLICY_UNAVAILABLE_CODE):
        return None
    details = event.get("errorDetails")
    if not isinstance(details, dict) or details.get("stage") not in _STAGES:
        return None
    ids = policy_ids_of(details["policyIds"]) if "policyIds" in details else ()
    if ids is None:
        return None
    if code == _POLICY_UNAVAILABLE_CODE or "reason" not in details:
        return PolicyDenial(code, details["stage"], ids)
    reason = details["reason"]
    if not _is_short_text(reason, _MAX_REASON_LENGTH):
        return None
    return PolicyDenial(code, details["stage"], ids, reason)
