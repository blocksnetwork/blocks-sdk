"""Cross-SDK parity test for billing_mode -> keyset routing.

Mirrors the Node SDK's ``tests/task-client-create.test.ts`` parity block
and the backend's ``afui_mvp_backend/src/lib/pubnub.ts`` ``BILLING_MODE_TO_KEYSET``
map. All three layers (Node SDK, Python SDK, backend) must agree:

    free -> playground keyset
    paid -> network keyset

Any divergence between these three sources of truth will split routing
and messages will land on the wrong keyset.

Phase 3 of the Billing Mode Contract initiative also adds wire-shape
parity for ``BillingModeMismatch`` typed errors: the same canonical
backend JSON-RPC error envelope must deserialize to a typed exception
with matching attributes in both Node and Python SDKs.
"""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.cdm_config import CdmApiConfig, CdmConfig, CdmKeyset
from blocks_network.rpc_client import (
    BillingModeMismatchError,
    RpcError,
    call_rpc,
)
from blocks_network.task_client import TaskClient


# Python-side parity map — mirrors Node's BILLING_MODE_TO_KEYSET_ENV constant
# and the backend's BILLING_MODE_TO_KEYSET map. Do not add or remove keys
# here without also updating the Node test and the backend.
BILLING_MODE_TO_KEYSET = {
    "free": "playground",
    "paid": "network",
}


def _make_cdm() -> CdmConfig:
    return CdmConfig(
        playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
        network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
        api=CdmApiConfig(base_url="https://api.example.com"),
    )


class TestBillingModeParity:
    """Parity checks between Python SDK, Node SDK, and the backend."""

    def test_parity_table_has_exactly_two_entries(self) -> None:
        assert set(BILLING_MODE_TO_KEYSET.keys()) == {"free", "paid"}
        assert len(BILLING_MODE_TO_KEYSET) == 2

    def test_parity_table_free_routes_to_playground(self) -> None:
        assert BILLING_MODE_TO_KEYSET["free"] == "playground"

    def test_parity_table_paid_routes_to_network(self) -> None:
        assert BILLING_MODE_TO_KEYSET["paid"] == "network"

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_task_client_free_resolves_playground_keyset(self, mock_cdm) -> None:
        mock_cdm.return_value = _make_cdm()

        client = TaskClient.create(billing_mode="free")

        assert client._subscribe_key == "pg-sub"
        assert client._publish_key == "pg-pub"

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_task_client_paid_resolves_network_keyset(self, mock_cdm) -> None:
        mock_cdm.return_value = _make_cdm()

        client = TaskClient.create(billing_mode="paid")

        assert client._subscribe_key == "nw-sub"
        assert client._publish_key == "nw-pub"


class TestTaskClientCreateRequiresBillingMode:
    """``TaskClient.create`` without ``billing_mode`` must raise.

    Mirrors the Node parity test's ``missing billingMode`` case. Python
    raises ``TypeError`` from the signature (positional-required arg)
    rather than a custom ``ValueError`` string, which is idiomatic Python.
    """

    def test_task_client_rejects_missing_billing_mode(self) -> None:
        with pytest.raises(TypeError):
            TaskClient.create()  # type: ignore[call-arg]

    def test_task_client_rejects_invalid_billing_mode(self) -> None:
        with pytest.raises(ValueError, match="billing_mode must be"):
            TaskClient.create(billing_mode="invalid")  # type: ignore[arg-type]


# ============================================================================
# Cross-language wire-shape parity for BillingModeMismatch error
# ============================================================================


# Canonical backend JSON-RPC error envelope for BillingModeMismatch.
# This payload shape is normative — the Node SDK's parity test must use
# the SAME literal envelope and assert the same expected/got values.
# Source: backend `bmc-data` Phase 1 IMPL_REPORT, "JSON-RPC error.data wire shape".
BILLING_MODE_MISMATCH_FIXTURE = {
    "jsonrpc": "2.0",
    "id": "rpc-fixture-id",
    "error": {
        "code": -32000,
        "message": (
            "Billing mode mismatch: caller declared 'free', agent is 'paid'. "
            "Read the agent's billingMode from the registry "
            "(Node: (await getAgent(name)).billingMode; Python: get_agent(agent_name).billing_mode) "
            "and pass it into TaskClient.create."
        ),
        "data": {
            "code": "BillingModeMismatch",
            "details": {"expected": "paid", "got": "free"},
        },
    },
}


def _mock_rpc_response(body: dict) -> MagicMock:
    encoded = json.dumps(body).encode("utf-8")
    resp = MagicMock()
    resp.read.return_value = encoded
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


class TestBillingModeMismatchWireParity:
    """The canonical wire fixture deserializes to the typed exception.

    Node SDK's parity test must use ``BILLING_MODE_MISMATCH_FIXTURE``
    above (or a structurally identical envelope) and assert the same
    ``expected`` / ``got`` values via Node's ``BillingModeMismatchError``.
    Wire-level fields are normative — language-level type names are not.
    """

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_canonical_envelope_maps_to_typed_exception(self, mock_urlopen) -> None:
        mock_urlopen.return_value = _mock_rpc_response(BILLING_MODE_MISMATCH_FIXTURE)

        with pytest.raises(BillingModeMismatchError) as exc_info:
            call_rpc(
                "sub-c-test",
                "SendMessage",
                {"agentName": "echo", "billingMode": "free"},
                base_url="http://localhost:3001",
            )

        # Wire-level shape parity (Node must assert these EXACT values from
        # the same fixture):
        assert exc_info.value.expected == "paid"
        assert exc_info.value.got == "free"

        # Cross-language type semantics: both SDKs MUST extend their RPC
        # error base class so generic RPC error catchers still see the
        # mismatch. Node: ``extends RpcError``. Python: ``extends RpcError``.
        # Q5 resolution.
        assert isinstance(exc_info.value, RpcError)

    def test_fixture_shape_normative(self) -> None:
        """Lock the fixture's normative wire shape.

        If this test changes, the Node SDK parity test fixture and any
        backend code emitting BillingModeMismatch must be updated in
        lock-step (and so must `bmc-data`'s IMPL_REPORT.md).
        """
        env = BILLING_MODE_MISMATCH_FIXTURE["error"]
        assert env["code"] == -32000
        data = env["data"]
        assert data["code"] == "BillingModeMismatch"
        assert set(data["details"].keys()) == {"expected", "got"}
        assert data["details"]["expected"] in ("free", "paid")
        assert data["details"]["got"] in ("free", "paid")

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_send_message_payload_includes_billing_mode_camelcase(
        self, mock_urlopen, mock_create_pn
    ) -> None:
        """SendMessage RPC params include camelCase ``billingMode`` on every send.

        Wire field name parity: Node sends ``billingMode``, Python sends
        ``billingMode``. Python parameter name (snake_case
        ``billing_mode``) is internal-only.
        """
        success_body = {
            "jsonrpc": "2.0",
            "id": "x",
            "result": {
                "taskId": "task-parity",
                "extensions": {
                    "blocks": {
                        "streamChannels": {"status": "u.user-1.task-parity"},
                        "readToken": "T4",
                    }
                },
            },
        }
        mock_urlopen.return_value = _mock_rpc_response(success_body)

        # Stub session pubnub so send_message doesn't try to subscribe.
        fake_pn = MagicMock()
        fake_pn.set_token = MagicMock()
        sub_builder = MagicMock()
        sub_builder.channels = lambda chs: sub_builder
        sub_builder.execute = lambda: None
        fake_pn.subscribe = lambda: sub_builder
        mock_create_pn.return_value = fake_pn

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="paid",
            base_url="http://localhost:3001",
        )
        try:
            client.send_message(
                agent_name="agent-b", request_parts=[], owner_id="user-1"
            )
        except Exception:
            pass  # We only care about the wire payload.

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        # camelCase wire field
        assert body["params"]["billingMode"] == "paid"
        # snake_case must NOT leak onto the wire
        assert "billing_mode" not in body["params"]
