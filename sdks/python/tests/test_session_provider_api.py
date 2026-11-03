"""The provider session endpoints' parsing and acknowledgement rules, over a faked transport."""

from __future__ import annotations

from typing import Any, Dict, List, Optional, Tuple

import pytest

from blocks_network._session.provider_api import ProviderApiError, ProviderSessionApi

from tests.session_provider_fakes import manifest_body, route_grant_body


def answering(body: Any, status: int = 200, base_url: str = "http://backend.test"):
    calls: List[Tuple[str, str, Optional[Dict[str, Any]]]] = []

    def request(method: str, url: str, sent: Optional[Dict[str, Any]]) -> Tuple[int, Any]:
        calls.append((method, url, sent))
        return status, body

    return ProviderSessionApi(base_url, request), calls


@pytest.mark.parametrize("org_id", ["org-1", None])
def test_accepts_a_session_opened_by_a_caller_with_or_without_an_org(org_id) -> None:
    api, _ = answering({**manifest_body("s1"), "orgId": org_id})
    assert api.get_session("s1").org_id == org_id


@pytest.mark.parametrize("org_id", ["missing", "", 42])
def test_rejects_a_manifest_whose_org_id_is_missing_empty_or_not_a_string(org_id) -> None:
    body = manifest_body("s1")
    if org_id == "missing":
        del body["orgId"]
    else:
        body["orgId"] = org_id
    api, _ = answering(body)
    with pytest.raises(ValueError, match="incomplete manifest"):
        api.get_session("s1")


def test_joins_a_base_url_with_a_trailing_slash_without_doubling_it() -> None:
    api, calls = answering(manifest_body("s1"), base_url="http://backend.test/")
    api.get_session("s1")
    assert calls == [("GET", "http://backend.test/api/v1/provider/sessions/s1", None)]


def test_registers_a_route_with_the_instance_and_its_session_bound() -> None:
    api, calls = answering(route_grant_body())
    grant = api.register_route("i-1", 3)
    assert calls == [
        (
            "POST",
            "http://backend.test/api/v1/provider/routes",
            {"instanceId": "i-1", "maxSessions": 3},
        )
    ]
    assert grant.backend_transport_uuid == "uuid-backend"


def test_refuses_a_route_without_the_backend_uuid_that_backend_notices_come_from() -> None:
    body = route_grant_body()
    del body["backendTransportUuid"]
    api, _ = answering(body)
    with pytest.raises(ValueError, match="route is missing backendTransportUuid"):
        api.register_route("i-1", 1)


def test_raises_the_backend_error_code_on_a_refusal() -> None:
    api, _ = answering({"error": "off", "code": "SESSIONS_DISABLED"}, status=404)
    with pytest.raises(ProviderApiError) as caught:
        api.register_route("i-1", 1)
    assert (caught.value.status, caught.value.code) == (404, "SESSIONS_DISABLED")


BATCH = {
    "routeId": "route1",
    "idempotencyKey": "k1",
    "openSessions": [],
    "events": [],
    "probeEchoes": [],
}


def test_returns_on_the_acknowledgement_of_this_batch() -> None:
    api, _ = answering({"routeId": "route1", "idempotencyKey": "k1"})
    api.post_checkpoint(BATCH)


@pytest.mark.parametrize(
    "body",
    [
        {"routeId": "route1", "idempotencyKey": "k2"},
        {"routeId": "route2", "idempotencyKey": "k1"},
        {},
    ],
    ids=["another batch", "another route", "no body"],
)
def test_rejects_a_200_that_acknowledges_something_else_so_the_batch_is_retried(body) -> None:
    api, _ = answering(body)
    with pytest.raises(ValueError, match="not the acknowledgement of this batch"):
        api.post_checkpoint(BATCH)
