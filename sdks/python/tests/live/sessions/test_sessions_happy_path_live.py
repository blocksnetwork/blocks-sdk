"""Node mirrors: call.live.test.ts, multi-exchange.live.test.ts in sdks/node/tests/live/sessions/."""

from __future__ import annotations

from typing import Any, Iterator, List

import pytest

from tests.live.sessions.live_harness import (
    decode_echo,
    LIVE_API_KEY,
    PROGRESS_MESSAGE,
    echo_result,
    base_url,
    live_only,
    needs_client,
    make_card,
    publish_agent,
    remove_agents,
    sessions_on,
    state_name,
    when_routed,
)

pytestmark = [live_only, sessions_on, needs_client]

OUTCOME_TIMEOUT_S = 30.0
INPUTS = ["first", "second", "third"]


@pytest.fixture(scope="module")
def created_agents() -> Iterator[List[str]]:
    names: List[str] = []
    yield names
    remove_agents(names)


@pytest.fixture
def echo_provider(created_agents: List[str]) -> Iterator[str]:
    from blocks_network import start_agent_instance
    from blocks_network.types import AgentInstanceOptions

    agent_name = publish_agent("sess_py", created_agents)
    runtime = start_agent_instance(
        AgentInstanceOptions(
            card=make_card(agent_name),
            agent_name=agent_name,
            base_url=base_url(),
            on_exchange=lambda _ctx, exchange: echo_result(exchange),
        )
    )
    try:
        yield agent_name
    finally:
        runtime["stop"]()


@pytest.fixture
def client() -> Iterator[Any]:
    from blocks_network import BlocksClient

    blocks_client = BlocksClient.create(
        billing_mode="free", api_key=LIVE_API_KEY, base_url=base_url()
    )
    try:
        yield blocks_client
    finally:
        blocks_client.destroy()


def test_call_returns_the_outcome_of_one_exchange(client: Any, echo_provider: str) -> None:
    outcome = when_routed(lambda: client.call(echo_provider, "hello"))

    assert state_name(outcome.state) == "completed"
    assert len(outcome.artifacts) == 1
    echo = decode_echo(outcome.artifacts[0])
    assert echo == {"exchangeId": outcome.exchange_id, "text": "hello", "sdk": "python"}


def test_open_session_three_sends_close(client: Any, echo_provider: str) -> None:
    session = when_routed(lambda: client.open_session(echo_provider))
    assert state_name(session.state) == "ready"

    exchanges = [session.send(text) for text in INPUTS]
    outcomes = [ex.wait_for_outcome(timeout=OUTCOME_TIMEOUT_S) for ex in exchanges]
    session.close()

    assert [o.exchange_id for o in outcomes] == [ex.exchange_id for ex in exchanges]
    for text, exchange, outcome in zip(INPUTS, exchanges, outcomes):
        assert state_name(outcome.state) == "completed"
        assert decode_echo(outcome.artifacts[0]) == {
            "exchangeId": exchange.exchange_id,
            "text": text,
            "sdk": "python",
        }
    assert state_name(session.state) == "closed"


def test_exchange_reports_progress_before_its_outcome(client: Any, echo_provider: str) -> None:
    session = when_routed(lambda: client.open_session(echo_provider))
    exchange = session.send("hello")
    progress: List[Any] = []
    exchange.on_progress(lambda event: progress.append(event.message))

    outcome = exchange.wait_for_outcome(timeout=OUTCOME_TIMEOUT_S)
    session.close()

    assert state_name(outcome.state) == "completed"
    assert progress == [PROGRESS_MESSAGE]
    assert state_name(session.state) == "closed"
