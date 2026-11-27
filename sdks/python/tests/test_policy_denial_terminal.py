"""A task the policy gate stopped says why to its caller.

Parity with Node ``policy-denial-terminal.test.ts``; both read ``fixtures/policy-denial.json``.
"""

import json
import threading
import time
from pathlib import Path

import pytest

from blocks_network import PolicyDenial, policy_denial_from_terminal
from blocks_network.task_session import TaskEvent
from tests.test_consumer_simplify import _make_mock_pubnub, _make_session, _simulate_message

CASES = json.loads((Path(__file__).resolve().parents[3] / "fixtures" / "policy-denial.json").read_text())["cases"]


def _denial(expected):
    if expected is None:
        return None
    return PolicyDenial(
        expected["code"], expected["stage"], tuple(expected["policyIds"]), expected.get("reason")
    )


@pytest.mark.parametrize("case", CASES, ids=[c["name"] for c in CASES])
def test_policy_denial_from_terminal_matches_the_shared_fixtures(case):
    expected = _denial(case["expected"])
    assert policy_denial_from_terminal(case["event"]) == expected
    assert policy_denial_from_terminal(TaskEvent(case["event"])) == expected


def test_policy_denial_is_frozen():
    denial = policy_denial_from_terminal(next(c for c in CASES if c["expected"])["event"])
    with pytest.raises(AttributeError):
        denial.code = "policy_unavailable"
    assert denial.code == "policy_rejected"


@pytest.mark.parametrize("case", [c for c in CASES if c["expected"]], ids=lambda c: c["name"])
def test_wait_for_terminal_returns_the_terminal_of_a_policy_denial(case):
    pn = _make_mock_pubnub()
    session = _make_session(pubnub=pn)
    thread = threading.Thread(
        target=lambda: (time.sleep(0.05), _simulate_message(pn, "u.alice.task-1", case["event"]))
    )
    thread.start()

    assert session.wait_for_terminal(timeout=5).raw == case["event"]
    thread.join()


@pytest.mark.parametrize(
    "data, expected",
    [
        ({"type": "policy_applied", "policyIds": ["pv_org", "pv_agent"]}, ("pv_org", "pv_agent")),
        ({"type": "policy_applied", "policyIds": []}, ()),
        ({"type": "policy_applied", "policyIds": ["i" * 64] * 16}, ("i" * 64,) * 16),
        ({"type": "progress", "policyIds": ["pv_org"]}, None),
        ({"type": "policy_applied"}, None),
        ({"type": "policy_applied", "policyIds": "pv_org"}, None),
        ({"type": "policy_applied", "policyIds": ["pv_org", 3]}, None),
        ({"type": "policy_applied", "policyIds": [""]}, None),
        ({"type": "policy_applied", "policyIds": ["i" * 65]}, None),
        ({"type": "policy_applied", "policyIds": ["i"] * 17}, None),
    ],
)
def test_task_event_policy_ids(data, expected):
    assert TaskEvent(data).policy_ids == expected
