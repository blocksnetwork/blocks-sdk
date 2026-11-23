"""The per-task client view: calls made through it name the task they come from. Parity with Node."""

import json
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import MagicMock, patch

from blocks_network.agent_instance import start_agent_instance
from blocks_network.parent_task_client import PARENT_TASK_HEADER, task_client_for_parent
from blocks_network.task_client import TaskClient
from blocks_network.types import AgentInstanceOptions
from tests.conftest import minimal_card
from tests.test_agent_instance_p3 import _make_mock_pubnub, _simulate_start_task


def _sent(task_id):
    body = json.dumps(
        {
            "jsonrpc": "2.0",
            "id": "x",
            "result": {
                "taskId": task_id,
                "extensions": {"blocks": {"streamChannels": {"status": f"u.user-1.{task_id}"}, "readToken": "T4"}},
            },
        }
    ).encode("utf-8")
    resp = MagicMock()
    resp.read.return_value = body
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def _shared():
    return TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")


def _send(client):
    session = client.send_message(agent_name="agent-b", request_parts=[{"type": "text", "text": "hi"}], owner_id="user-1")
    session.close()


def _parent_headers(mock_urlopen):
    # urllib title-cases header names it stores.
    return [call[0][0].get_header(PARENT_TASK_HEADER.capitalize()) for call in mock_urlopen.call_args_list]


@patch("blocks_network.task_client.TaskClient._create_session_pubnub")
@patch("blocks_network.rpc_client.urllib.request.urlopen")
def test_names_the_parent_on_the_views_calls_and_not_on_the_shared_clients(mock_urlopen, mock_create_pn):
    mock_urlopen.side_effect = [_sent("child"), _sent("standalone")]
    mock_create_pn.return_value = MagicMock()
    client = _shared()

    _send(task_client_for_parent(client, "parent-1"))
    _send(client)

    assert _parent_headers(mock_urlopen) == ["parent-1", None]


@patch("blocks_network.task_client.TaskClient._create_session_pubnub")
@patch("blocks_network.rpc_client.urllib.request.urlopen")
def test_keeps_two_views_over_one_client_apart_when_they_send_at_once(mock_urlopen, mock_create_pn):
    mock_urlopen.side_effect = [_sent("a"), _sent("b")]
    mock_create_pn.return_value = MagicMock()
    client = _shared()

    with ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(lambda tid: _send(task_client_for_parent(client, tid)), ["task-of-u1", "task-of-u2"]))

    assert sorted(_parent_headers(mock_urlopen)) == ["task-of-u1", "task-of-u2"]


def test_the_handler_receives_a_client_naming_its_task_and_the_instance_client_names_none():
    pn = _make_mock_pubnub()
    seen = {}
    done = threading.Event()

    def handler(task, ctx):
        seen[task.task_id] = ctx.task_client._rpc_headers.get(PARENT_TASK_HEADER)
        done.set()

    with patch("blocks_network.agent_instance.create_pubnub_client") as create:
        create.return_value = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(card=minimal_card(), pubnub=pn, agent_name="test_agent", handler=handler)
        )
        time.sleep(0.2)
        _simulate_start_task(pn, task_id="task-of-u1")
        assert done.wait(timeout=2.0)

        assert seen == {"task-of-u1": "task-of-u1"}
        assert (result["task_client"]._rpc_headers or {}).get(PARENT_TASK_HEADER) is None
        result["stop"]()


def test_a_key_change_made_through_a_view_reaches_the_shared_client_and_another_view():
    client = _shared()

    task_client_for_parent(client, "parent-1").update_keys("sub-c-new", "pub-c-new")

    assert client._subscribe_key == "sub-c-new"
    assert task_client_for_parent(client, "parent-2")._subscribe_key == "sub-c-new"
