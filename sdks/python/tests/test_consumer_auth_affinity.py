"""Write-affinity wire-up tests for ConsumerAuth."""

from __future__ import annotations

import json
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.consumer_auth import ConsumerAuth
from blocks_network.write_affinity import capture_affinity, inject_affinity, reset_affinity


@pytest.fixture(autouse=True)
def _reset_affinity():
    reset_affinity()
    yield
    reset_affinity()


def _mock_response(payload: dict, affinity: str | None = None):
    resp = MagicMock()
    resp.read.return_value = json.dumps(payload).encode("utf-8")
    resp.status = 200
    resp.getheader = lambda name, default=None: (
        affinity if name == "X-Write-Affinity" and affinity is not None else default
    )
    resp.__enter__ = lambda s: s
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def test_captures_affinity_on_bootstrap_and_echoes_on_refresh():
    future = str(int(time.time()) + 60)
    sent_headers: list[dict[str, str]] = []

    def mock_urlopen(req, **kwargs):
        sent_headers.append(dict(req.headers))
        body = {
            "accessToken": "jwt",
            "refreshToken": "rt",
            "expiresIn": 60,
            "userId": "u",
        }
        return _mock_response(body, affinity=future)

    auth = ConsumerAuth(api_key="ck_test", base_url="http://localhost:3001")
    try:
        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.init()
            auth.on_auth_failure()
    finally:
        auth.destroy()

    assert len(sent_headers) == 2
    assert not any(k.lower() == "x-write-affinity" for k in sent_headers[0])
    assert {k.lower(): v for k, v in sent_headers[1].items()}["x-write-affinity"] == future


def test_does_not_inject_expired_affinity():
    past = str(int(time.time()) - 10)
    sent_headers: list[dict[str, str]] = []

    def mock_urlopen(req, **kwargs):
        sent_headers.append(dict(req.headers))
        body = {
            "accessToken": "jwt",
            "refreshToken": "rt",
            "expiresIn": 60,
            "userId": "u",
        }
        return _mock_response(body, affinity=past)

    auth = ConsumerAuth(api_key="ck_test", base_url="http://localhost:3001")
    try:
        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.init()
            auth.on_auth_failure()
    finally:
        auth.destroy()

    assert len(sent_headers) == 2
    assert not any(k.lower() == "x-write-affinity" for k in sent_headers[1])


def test_token_endpoint_mode_does_not_exchange_affinity():
    """Mode-2 traffic goes to a customer-owned proxy — no affinity in or out."""
    future = str(int(time.time()) + 60)

    prior = MagicMock()
    prior.getheader = lambda name, default=None: (
        future if name == "X-Write-Affinity" else default
    )
    capture_affinity(prior)

    proxy_attempt_affinity = str(int(time.time()) + 300)
    sent_headers: list[dict[str, str]] = []

    def mock_urlopen(req, **kwargs):
        sent_headers.append(dict(req.headers))
        resp = MagicMock()
        resp.read.return_value = json.dumps(
            {"token": "jwt", "expiresIn": 60, "userId": "u"}
        ).encode("utf-8")
        resp.status = 200
        resp.getheader = lambda name, default=None: (
            proxy_attempt_affinity if name == "X-Write-Affinity" else default
        )
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        return resp

    auth = ConsumerAuth(
        token_endpoint="https://customer-proxy.example/token",
        base_url="http://localhost:3001",
    )
    try:
        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.init()
    finally:
        auth.destroy()

    assert len(sent_headers) == 1
    assert not any(k.lower() == "x-write-affinity" for k in sent_headers[0])

    out: dict[str, str] = {}
    inject_affinity(out)
    assert out["X-Write-Affinity"] == future
