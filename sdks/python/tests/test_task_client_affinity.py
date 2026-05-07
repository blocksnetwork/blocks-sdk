"""Write-affinity wire-up tests for TaskClient._fetch_task_read_token."""

from __future__ import annotations

import json
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.task_client import TaskClient
from blocks_network.write_affinity import capture_affinity, reset_affinity


@pytest.fixture(autouse=True)
def _reset_affinity():
    reset_affinity()
    yield
    reset_affinity()


def _token_response():
    resp = MagicMock()
    body = {
        "pamToken": "pam-test",
        "channel": "u.org.task",
        "ttlMinutes": 5,
    }
    resp.read.return_value = json.dumps(body).encode("utf-8")
    resp.status = 200
    resp.getheader = lambda name, default=None: default
    resp.__enter__ = lambda s: s
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def test_preserves_prior_affinity_across_token_calls():
    future = str(int(time.time()) + 60)
    prior = MagicMock()
    prior.getheader = lambda name, default=None: (
        future if name == "X-Write-Affinity" else default
    )
    capture_affinity(prior)

    sent_headers: list[dict[str, str]] = []

    def mock_urlopen(req, **kwargs):
        sent_headers.append(dict(req.headers))
        return _token_response()

    client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
    with patch("urllib.request.urlopen", side_effect=mock_urlopen):
        client._fetch_task_read_token("task-1")
        client._fetch_task_read_token("task-2")

    assert len(sent_headers) == 2
    assert {k.lower(): v for k, v in sent_headers[0].items()}["x-write-affinity"] == future
    assert {k.lower(): v for k, v in sent_headers[1].items()}["x-write-affinity"] == future
