"""Node mirror: tests/session-fixtures-parity.test.ts. Both load every case, no skip list."""

from __future__ import annotations

import json
import logging
from pathlib import Path
from typing import Any, Dict, List

import pytest

from blocks_network._session import provider_receiver
from blocks_network._session.consumer_receiver import (
    ConsumerReceiver,
    ExchangeOutcome,
    ExchangeProgress,
)
from blocks_network._session.counterpart import consumer_counterparts, provider_counterparts
from blocks_network._session.provider_receiver import ProviderReceiver
from blocks_network.protocol_version import CURRENT_PROTOCOL_VERSION

from tests.manual_scheduler import ManualScheduler

FIXTURES_DIR = Path(__file__).resolve().parents[3] / "fixtures" / "sessions"


def _read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


ENVELOPE: Dict[str, Any] = _read_json(FIXTURES_DIR / "envelope.json")
CASES: List[Dict[str, Any]] = [
    case
    for path in sorted((FIXTURES_DIR / "cases").glob("*.json"))
    for case in _read_json(path)["cases"]
]
_CASE_IDS = [case["id"] for case in CASES]


def _is_delivery(step: Dict[str, Any]) -> bool:
    return "from" in step


def _counterpart_accepts(receiver: str, sender: str, message_type: str) -> bool:
    allowed = ENVELOPE["accepts"][receiver].get(sender, [])
    return allowed == "*" or message_type in allowed


def _field_problems(rule: Dict[str, Any], message: Dict[str, Any]) -> List[str]:
    envelope_fields = rule.get("envelope", ENVELOPE["envelopeFields"])
    allowed = {*envelope_fields, *rule["required"], *rule["optional"]}
    missing = [f"missing {f}" for f in [*envelope_fields, *rule["required"]] if f not in message]
    return missing + [f"unknown field {f}" for f in message if f not in allowed]


def _identity_problems(message: Dict[str, Any]) -> List[str]:
    problems = []
    if message.get("protocolVersion") != ENVELOPE["protocolVersion"]:
        problems.append("protocolVersion")
    if "sessionId" in message and message["sessionId"] != ENVELOPE["sessionId"]:
        problems.append("sessionId")
    return problems


def _seq_problems(sender: str, message: Dict[str, Any]) -> List[str]:
    if "seq" not in message:
        return []
    seq = message["seq"]
    is_int = isinstance(seq, int) and not isinstance(seq, bool)
    valid = is_int and (seq == 0 if sender == "backend" else seq >= 1)
    return [] if valid else [f"seq {seq} from {sender}"]


def _exchange_id_problems(message: Dict[str, Any]) -> List[str]:
    ids = [message.get("exchangeId"), (message.get("exchange") or {}).get("exchangeId")]
    max_len = ENVELOPE["maxExchangeIdLength"]
    return [
        f"exchangeId {i}"
        for i in ids
        if i is not None and not (isinstance(i, str) and 0 < len(i) <= max_len)
    ]


def _enum_problems(rule: Dict[str, Any], message: Dict[str, Any]) -> List[str]:
    return [f for f, values in rule.get("enums", {}).items() if f in message and message[f] not in values]


def _message_problems(sender: str, message: Dict[str, Any]) -> List[str]:
    rule = ENVELOPE["types"].get(message["type"])
    if rule is None:
        return [f"unknown type {message['type']}"]
    return [
        *_field_problems(rule, message),
        *_identity_problems(message),
        *_seq_problems(sender, message),
        *_exchange_id_problems(message),
        *_enum_problems(rule, message),
    ]


def _header_problems(case: Dict[str, Any]) -> List[str]:
    receiver = case["receiver"]
    problems: List[str] = []
    if not case.get("summary"):
        problems.append("summary")
    if case["given"]["state"] not in ENVELOPE["givenStates"].get(receiver, []):
        problems.append("given.state")
    if not case["expect"]:
        problems.append("empty expect")
    problems += [f"expect.{k}" for k in case["expect"] if k not in ENVELOPE["expectKeys"][receiver]]
    states = [case["expect"].get("state"), *case["expect"].get("stateChanges", [])]
    problems += [f"state {s}" for s in states if s is not None and s not in ENVELOPE["sessionStates"]]
    if not case["steps"]:
        problems.append("no steps")
    return problems


def _delivery_problems(receiver: str, step: Dict[str, Any], processed: set) -> List[str]:
    sender, message = step["from"], step["message"]
    problems = [] if sender in ENVELOPE["parties"] else ["from"]
    problems += _message_problems(sender, message)
    if not _counterpart_accepts(receiver, sender, message["type"]):
        return problems if step.get("expect") == "drop" else [*problems, "non-counterpart must drop"]
    key = (sender, message.get("seq"))
    # Backend notices all carry seq 0 and are exempt from dedup.
    duplicate = "seq" in message and sender != "backend" and key in processed
    processed.add(key)
    if step.get("expect") == "drop" and not duplicate:
        problems.append("drop without cause")
    if step.get("expect") == "dispatch" and duplicate:
        problems.append("duplicate dispatched")
    return problems


def _advance_ok(step: Dict[str, Any]) -> bool:
    advance = step["advanceMs"]
    return isinstance(advance, int) and not isinstance(advance, bool) and advance > 0


def _case_problems(case: Dict[str, Any]) -> List[str]:
    processed: set = set()
    step_problems: List[str] = []
    for i, step in enumerate(case["steps"]):
        if _is_delivery(step):
            step_problems += [f"step {i}: {p}" for p in _delivery_problems(case["receiver"], step, processed)]
        elif not _advance_ok(step):
            step_problems.append(f"step {i}")
    return [*_header_problems(case), *step_problems]


class TestSessionFixtures:
    def test_declare_unique_case_ids(self) -> None:
        assert _CASE_IDS
        assert len(set(_CASE_IDS)) == len(_CASE_IDS)

    def test_use_the_protocol_version_the_sdk_publishes(self) -> None:
        assert ENVELOPE["protocolVersion"] == CURRENT_PROTOCOL_VERSION

    @pytest.mark.parametrize("case", CASES, ids=_CASE_IDS)
    def test_is_well_formed(self, case: Dict[str, Any]) -> None:
        assert _case_problems(case) == []


def _outcome_view(outcome: ExchangeOutcome) -> Dict[str, Any]:
    view: Dict[str, Any] = {"exchangeId": outcome.exchange_id, "state": outcome.state}
    if outcome.error is not None:
        view["error"] = outcome.error
    if outcome.reason is not None:
        view["reason"] = outcome.reason
    view["artifacts"] = outcome.artifacts
    return view


def _progress_view(progress: ExchangeProgress) -> Dict[str, Any]:
    view: Dict[str, Any] = {"exchangeId": progress.exchange_id}
    if progress.message is not None:
        view["message"] = progress.message
    if progress.progress is not None:
        view["progress"] = progress.progress
    return view


class _ConsumerHarness:
    def __init__(self, given: Dict[str, Any], parties: Dict[str, str], session_id: str) -> None:
        self._scheduler = ManualScheduler()
        self._recover_calls = 0
        self._state_changes: List[str] = []
        self._outcomes: List[Dict[str, Any]] = []
        self._progress: List[Dict[str, Any]] = []
        self._receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=session_id,
                provider_uuid=parties["provider"],
                backend_uuid=parties["backend"],
            ),
            scheduler=self._scheduler,
            recover=self._recover,
            state=given["state"],
            on_state_change=self._state_changes.append,
            on_progress=lambda p: self._progress.append(_progress_view(p)),
            on_outcome=lambda o: self._outcomes.append(_outcome_view(o)),
        )
        for exchange_id in given["pendingExchanges"]:
            self._receiver.track_exchange(exchange_id)

    def _recover(self) -> None:
        self._recover_calls += 1

    def deliver(self, publisher: str, message: Dict[str, Any]) -> bool:
        return self._receiver.deliver(publisher, message)

    def advance(self, ms: int) -> None:
        self._scheduler.advance(ms)

    def observed(self) -> Dict[str, Any]:
        return {
            "state": self._receiver.state,
            "stateChanges": self._state_changes,
            "outcomes": self._outcomes,
            "progress": self._progress,
            "recoverCalls": self._recover_calls,
        }


class _GapLog(logging.Handler):
    def __init__(self) -> None:
        super().__init__(logging.WARNING)
        self.detected = False

    def emit(self, record: logging.LogRecord) -> None:
        self.detected = True


class _ProviderHarness:
    def __init__(self, given: Dict[str, Any], parties: Dict[str, str], session_id: str) -> None:
        self._exchanges_started: List[str] = []
        self._probe_echoes: List[str] = []
        self._receiver = ProviderReceiver(
            counterparts=provider_counterparts(
                session_id=session_id,
                consumer_uuid=parties["consumer"],
                backend_uuid=parties["backend"],
            ),
            state=given["state"],
            # Nothing is ever in flight here, so the close completes at once.
            on_close_requested=lambda _: self._receiver.terminate("closed"),
            on_exchange=lambda e: self._exchanges_started.append(e.exchange_id),
            on_probe=self._probe_echoes.append,
        )
        self._gap_log = _GapLog()

    def deliver(self, publisher: str, message: Dict[str, Any]) -> bool:
        provider_logger = logging.getLogger(provider_receiver.__name__)
        provider_logger.addHandler(self._gap_log)
        try:
            return self._receiver.deliver(publisher, message)
        finally:
            provider_logger.removeHandler(self._gap_log)

    def advance(self, ms: int) -> None:
        pass

    def observed(self) -> Dict[str, Any]:
        return {
            "state": self._receiver.state,
            "exchangesStarted": self._exchanges_started,
            "probeEchoes": self._probe_echoes,
            "gapDetected": self._gap_log.detected,
        }


def _create_receiver_harness(receiver: str, given: Dict[str, Any], context: Dict[str, Any]) -> Any:
    """Same shape as the Node ReceiverHarness in tests/session-fixtures-parity.test.ts.

    ``recover()`` finds no history, so a gap that triggers it is skipped.
    """
    harness = _ConsumerHarness if receiver == "consumer" else _ProviderHarness
    return harness(given, context["parties"], context["sessionId"])


@pytest.mark.parametrize("case", CASES, ids=_CASE_IDS)
def test_sdk_against_session_fixture(case: Dict[str, Any]) -> None:
    harness = _create_receiver_harness(case["receiver"], case["given"], ENVELOPE)
    for step in case["steps"]:
        if not _is_delivery(step):
            harness.advance(step["advanceMs"])
            continue
        dispatched = harness.deliver(ENVELOPE["parties"][step["from"]], step["message"])
        if "expect" in step:
            assert dispatched is (step["expect"] == "dispatch")
    observed = harness.observed()
    for key, value in case["expect"].items():
        assert observed[key] == value, key
