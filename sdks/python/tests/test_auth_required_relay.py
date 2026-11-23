"""A task that fails because its user must connect or approve an integration says so to its caller.

Parity with Node ``auth-required-relay.test.ts`` and ``agent-instance-auth-required.test.ts``.
"""

import threading
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network import AuthRequiredError, ConnectionRequiredError, RelayedAuthRequiredError
from blocks_network.agent_instance import start_agent_instance
from blocks_network.auth_provider import StaticAuthProvider
from blocks_network.auth_required_terminal import auth_required_from_terminal
from blocks_network.task_client import TaskClient
from blocks_network.types import AgentInstanceOptions
from tests import test_agent_instance as provider
from tests.conftest import minimal_card
from tests.test_consumer_simplify import _make_mock_pubnub, _make_session, _simulate_message
from tests.test_task_client import _mock_urlopen_response, create_fake_pubnub

DETAILS = {
    "integration": "salesforce",
    "reason": "not_approved",
    "connectUrl": "https://app.example/connect/org-1/salesforce?agent=b&via=a",
}
REFUSED = {
    "type": "terminal",
    "taskId": "task-1",
    "state": "failed",
    "error": "Integration connection required",
    "errorCode": "AuthRequired",
    "errorDetails": DETAILS,
}


def _deliver_soon(pn, message):
    thread = threading.Thread(
        target=lambda: (time.sleep(0.05), _simulate_message(pn, "u.alice.task-1", message)),
    )
    thread.start()
    return thread


def test_wait_for_terminal_raises_the_relayed_refusal_and_on_terminal_still_gets_the_event():
    pn = _make_mock_pubnub()
    session = _make_session(pubnub=pn)
    seen = []
    session.on_terminal(lambda event: seen.append(event.raw))
    thread = _deliver_soon(pn, REFUSED)

    with pytest.raises(RelayedAuthRequiredError) as raised:
        session.wait_for_terminal(timeout=5)
    thread.join()

    err = raised.value
    assert (err.integration, err.reason, err.connect_url) == tuple(DETAILS.values())
    assert str(err) == "Integration connection required"
    # Not the class a backend refusal raises, so code that trusts that one's link does not pick this one up.
    assert not isinstance(err, AuthRequiredError)
    assert seen == [REFUSED]


@pytest.mark.parametrize(
    "event",
    [
        {k: v for k, v in REFUSED.items() if k not in ("errorCode", "errorDetails")},
        {**REFUSED, "errorCode": "Other"},
        {**REFUSED, "errorDetails": {"integration": "x", "reason": "y"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "javascript:alert(1)"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "http://[broken"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "https:evil.example/x"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "http://ex ample.com/x"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "http://?x"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "https://example.com:bad"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": "http://user@/x"}},
        {**REFUSED, "errorDetails": {**DETAILS, "connectUrl": DETAILS["connectUrl"] + "\n"}},
        {**REFUSED, "errorDetails": {**DETAILS, "integration": "x" * 129}},
        {**REFUSED, "state": "completed"},
    ],
    ids=[
        "another failure",
        "an unknown code",
        "details missing the link",
        "a link that is not http(s)",
        "a link that does not parse",
        "a link a URL parser would repair",
        "a link with a space in it",
        "a link with no host",
        "a link with a bad port",
        "a link with only userinfo",
        "a link with a trailing newline",
        "an oversized field",
        "a task that completed",
    ],
)
def test_wait_for_terminal_returns_the_event_for(event):
    pn = _make_mock_pubnub()
    session = _make_session(pubnub=pn)
    thread = _deliver_soon(pn, event)

    assert session.wait_for_terminal(timeout=5).raw == event
    thread.join()
    assert auth_required_from_terminal(event) is None


def _send_with_history(mock_urlopen, mock_create_pn, terminal):
    mock_urlopen.return_value = _mock_urlopen_response({
        "taskId": "task-1",
        "extensions": {"blocks": {"streamChannels": {"status": "u.user-1.task-1"}, "readToken": "T4"}},
    })
    in_history = MagicMock()
    in_history.message = terminal
    in_history.timetoken = "17000000000000001"
    mock_create_pn.return_value = create_fake_pubnub(history_messages=[in_history])["pubnub"]
    client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
    return client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1")


@patch("blocks_network.task_client.TaskClient._create_session_pubnub")
@patch("blocks_network.rpc_client.urllib.request.urlopen")
def test_a_task_refused_before_the_caller_subscribed_still_raises(mock_urlopen, mock_create_pn):
    session = _send_with_history(mock_urlopen, mock_create_pn, REFUSED)

    with pytest.raises(RelayedAuthRequiredError) as raised:
        session.wait_for_terminal(timeout=1)
    assert raised.value.connect_url == DETAILS["connectUrl"]
    seen = []
    session.on_terminal(lambda event: seen.append(event.raw))
    assert seen == [REFUSED]
    session.close()


@patch("blocks_network.task_client.TaskClient._create_session_pubnub")
@patch("blocks_network.rpc_client.urllib.request.urlopen")
def test_an_oversized_message_is_cut_so_a_called_agent_cannot_size_its_callers_terminal(mock_urlopen, mock_create_pn):
    session = _send_with_history(mock_urlopen, mock_create_pn, {**REFUSED, "error": "x" * 5000})

    with pytest.raises(RelayedAuthRequiredError) as raised:
        session.wait_for_terminal(timeout=1)
    assert len(str(raised.value)) == 500
    session.close()


@patch("blocks_network.task_client.call_rpc")
def test_connect_to_a_finished_task_hands_over_historys_terminal_so_the_refusal_still_raises(mock_rpc):
    mock_rpc.return_value = {
        "task": {"taskId": "task-1", "agentName": "agent-b", "owner": "alice", "state": "failed"},
    }
    in_history = MagicMock()
    in_history.message = REFUSED
    in_history.timetoken = "17000000000000001"
    pn = create_fake_pubnub(history_messages=[in_history])["pubnub"]
    client = TaskClient(
        subscribe_key="sub-key",
        billing_mode="free",
        auth_provider=StaticAuthProvider("jwt-token"),
        base_url="http://localhost:3000",
    )
    token = {"pamToken": "t4", "channel": "u.org1.task-1", "ttlMinutes": 60}
    with patch.object(client, "_create_session_pubnub", return_value=pn), \
         patch.object(client, "_fetch_task_read_token", return_value=token):
        session = client.connect("task-1")

    with pytest.raises(RelayedAuthRequiredError) as raised:
        session.wait_for_terminal(timeout=1)
    assert raised.value.connect_url == DETAILS["connectUrl"]
    session.close()


@patch("blocks_network.task_client.TaskClient._create_session_pubnub")
@patch("blocks_network.rpc_client.urllib.request.urlopen")
def test_a_task_that_failed_before_the_caller_subscribed_keeps_its_message(mock_urlopen, mock_create_pn):
    failed = {"type": "terminal", "taskId": "task-1", "state": "failed", "error": "boom"}
    session = _send_with_history(mock_urlopen, mock_create_pn, failed)

    assert session.wait_for_terminal(timeout=1).get("error") == "boom"
    session.close()


def _terminal_after_handler_raises(exc):
    pn = provider._make_mock_pubnub()
    per_task = provider._make_mock_pubnub()
    published = []

    def _tracking_publish():
        chain = MagicMock()
        record = {}
        chain.channel = lambda ch: chain
        chain.message = lambda msg: (record.__setitem__("message", msg), chain)[1]
        chain.meta = lambda m: chain
        chain.should_store = lambda v: chain
        chain.use_post = lambda v: chain
        chain.sync = lambda: (published.append(record["message"]), MagicMock())[1]
        return chain

    per_task.publish = _tracking_publish

    def handler(task, ctx):
        raise exc

    with patch("blocks_network.agent_instance.create_pubnub_client", return_value=per_task):
        result = start_agent_instance(
            AgentInstanceOptions(card=minimal_card(), pubnub=pn, agent_name="acme_echo", handler=handler)
        )
        time.sleep(0.2)
        provider._simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1", "agentName": "acme_echo",
            "ownerId": "alice", "taskKind": "request", "hasStream": False,
        })
        deadline = time.time() + 3
        while time.time() < deadline and not any(m.get("state") == "failed" for m in published):
            time.sleep(0.02)
        result["stop"]()
    failed = [m for m in published if m.get("state") == "failed"]
    assert failed, "the handler's failure was never published"
    return failed[0]


@pytest.mark.parametrize(
    ("exc", "error"),
    [
        (
            ConnectionRequiredError(DETAILS["integration"], DETAILS["reason"], DETAILS["connectUrl"]),
            "Integration connection required: salesforce (not_approved)",
        ),
        (
            AuthRequiredError("Approval required", DETAILS["integration"], DETAILS["reason"], DETAILS["connectUrl"]),
            "Approval required",
        ),
        (
            RelayedAuthRequiredError("Relayed", DETAILS["integration"], DETAILS["reason"], DETAILS["connectUrl"]),
            "Relayed",
        ),
    ],
    ids=[
        "its own connection being refused",
        "an agent it called being refused",
        "a refusal relayed from further down the chain",
    ],
)
def test_a_handler_error_asking_for_approval_fails_the_task_with_the_code_and_details(exc, error):
    terminal = _terminal_after_handler_raises(exc)

    assert terminal["error"] == error
    assert terminal["errorCode"] == "AuthRequired"
    assert terminal["errorDetails"] == DETAILS


@pytest.mark.parametrize(
    "exc",
    [
        ValueError("boom"),
        ConnectionRequiredError(DETAILS["integration"], DETAILS["reason"], "javascript:alert(1)"),
        ConnectionRequiredError(DETAILS["integration"], DETAILS["reason"], "http://[broken"),
    ],
    ids=["any other error", "an approval error whose link is not http(s)", "one whose link does not parse"],
)
def test_a_handler_error_fails_the_task_with_only_its_message_for(exc):
    terminal = _terminal_after_handler_raises(exc)

    assert terminal["error"] == str(exc)
    assert "errorCode" not in terminal
    assert "errorDetails" not in terminal
