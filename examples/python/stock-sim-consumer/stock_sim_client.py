"""
Shared client library for the stock-sim-consumer example.

Provides request parsing, interactive prompting, and the main
runStockSimTask function that submits a pipe task to stock-sim
and consumes the resulting stream.
"""

from __future__ import annotations

import json
import sys
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional

from blocks_network import TaskClient, SendMessageParams

DEFAULT_SYMBOLS = "AAPL,MSFT,NVDA"
DEFAULT_DURATION_MINUTES = 1
PROVIDERS = {"node": "stock-sim", "python": "stock-sim-python"}
DEFAULT_PROVIDER = "python"


@dataclass
class StockQuote:
    type: str
    symbol: str
    price: float
    change: float
    tick: int
    at: str


@dataclass
class StockRequest:
    symbols_input: str = DEFAULT_SYMBOLS
    symbols: List[str] = field(default_factory=lambda: DEFAULT_SYMBOLS.split(","))
    duration_minutes: int = DEFAULT_DURATION_MINUTES
    provider: str = DEFAULT_PROVIDER


@dataclass
class StockStreamResult:
    provider_task_id: str = ""
    symbols: List[str] = field(default_factory=list)
    duration_minutes: int = 0
    quotes_received: int = 0
    last_quotes: Dict[str, StockQuote] = field(default_factory=dict)


def parse_stock_request(parts: Optional[List[Any]]) -> Dict[str, Any]:
    if not parts:
        return {}

    result: Dict[str, Any] = {}
    for part in parts:
        if isinstance(part, str):
            result["symbols_input"] = part
        elif isinstance(part, dict):
            # The task:send script wraps --message as {kind: "input_text", text: "..."},
            # so try to JSON-parse the text field to extract structured fields.
            text_val = part.get("text")
            if isinstance(text_val, str):
                try:
                    parsed = json.loads(text_val)
                    if isinstance(parsed, dict):
                        part = {**part, **parsed}
                except (json.JSONDecodeError, ValueError):
                    result["symbols_input"] = text_val
            if isinstance(part.get("symbols"), str):
                result["symbols_input"] = part["symbols"]
            elif isinstance(part.get("symbols"), list):
                result["symbols_input"] = ",".join(
                    s for s in part["symbols"] if isinstance(s, str)
                )
            elif isinstance(part.get("text"), str) and "symbols_input" not in result:
                result["symbols_input"] = part["text"]
            if isinstance(part.get("durationMinutes"), (int, float)):
                result["duration_minutes"] = _normalize_duration(part["durationMinutes"])
            if isinstance(part.get("duration"), (int, float)):
                result["duration_minutes"] = _normalize_duration(part["duration"])
            if isinstance(part.get("provider"), str) and part["provider"] in PROVIDERS:
                result["provider"] = part["provider"]
    return result


def finalize_stock_request(initial: Optional[Dict[str, Any]] = None) -> StockRequest:
    initial = initial or {}
    symbols_input = (initial.get("symbols_input") or DEFAULT_SYMBOLS).strip() or DEFAULT_SYMBOLS
    symbols = _normalize_symbols(symbols_input)

    return StockRequest(
        symbols_input=",".join(symbols),
        symbols=symbols,
        duration_minutes=initial.get("duration_minutes", DEFAULT_DURATION_MINUTES),
        provider=initial.get("provider", DEFAULT_PROVIDER),
    )


def prompt_for_stock_request(initial: Optional[Dict[str, Any]] = None) -> StockRequest:
    defaults = finalize_stock_request(initial)

    raw_symbols = input(f"Symbols (comma-separated) [{defaults.symbols_input}]: ").strip()
    symbols_input = raw_symbols or defaults.symbols_input

    raw_duration = input(f"Duration in minutes [{defaults.duration_minutes}]: ").strip()
    duration = (
        _normalize_duration(int(raw_duration))
        if raw_duration
        else defaults.duration_minutes
    )

    raw_provider = input(f"Provider (node/python) [{defaults.provider}]: ").strip().lower()
    provider = raw_provider if raw_provider in PROVIDERS else defaults.provider

    return finalize_stock_request({
        "symbols_input": symbols_input,
        "duration_minutes": duration,
        "provider": provider,
    })


def run_stock_sim_task(
    task_client: TaskClient,
    request: StockRequest,
    log: Optional[Callable[[str], None]] = None,
) -> StockStreamResult:
    agent_name = PROVIDERS.get(request.provider, PROVIDERS[DEFAULT_PROVIDER])
    # owner_id is omitted from SendMessageParams: the server validates it
    # against the authenticated user behind the auth token, so the SDK's
    # default (derived from auth identity) is the only safe value.
    session = task_client.send_message(SendMessageParams(
        agent_name=agent_name,
        task_kind="pipe",
        duration=request.duration_minutes,
        request_parts=[{"partId": "symbols", "text": request.symbols_input}],
    ))

    if log:
        log(
            f"Submitted pipe task {session.task_id} for {', '.join(request.symbols)} "
            f"({request.duration_minutes} minute{'s' if request.duration_minutes != 1 else ''})"
        )

    last_quotes: Dict[str, StockQuote] = {}
    quotes_received = 0

    stream_ref = session.wait_for_stream(timeout=30.0)
    if log:
        log(f"Opened stream {stream_ref.descriptor.stream_id}")

    stream = stream_ref.open()

    # stock-sim emits `format: "events"`. `stream.events()` flattens the
    # per-envelope batches and yields one quote per yield; reach for the
    # lower-level `stream.inbound` iterator only when you need raw
    # envelope metadata (`seq`, `ts`, `encoding`).
    for event in stream.events():
        for quote in _normalize_quotes(event):
            last_quotes[quote.symbol] = quote
            quotes_received += 1
            if log:
                sign = "+" if quote.change >= 0 else ""
                log(
                    f"[{quote.at}] {quote.symbol} ${quote.price:.2f} "
                    f"({sign}{quote.change:.2f})"
                )

    session.close()

    return StockStreamResult(
        provider_task_id=session.task_id,
        symbols=request.symbols,
        duration_minutes=request.duration_minutes,
        quotes_received=quotes_received,
        last_quotes=last_quotes,
    )


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _normalize_symbols(raw: str) -> List[str]:
    seen: set = set()
    result: List[str] = []
    for s in raw.split(","):
        s = s.strip().upper()
        if s and s not in seen:
            seen.add(s)
            result.append(s)
    return result if result else DEFAULT_SYMBOLS.split(",")


def _normalize_duration(value: Any) -> int:
    if isinstance(value, (int, float)) and value > 0:
        return max(1, int(value))
    return DEFAULT_DURATION_MINUTES


def _normalize_quotes(data: Any) -> List[StockQuote]:
    if isinstance(data, list):
        result: List[StockQuote] = []
        for entry in data:
            result.extend(_normalize_quotes(entry))
        return result

    if _is_quote(data):
        return [StockQuote(
            type=data["type"],
            symbol=data["symbol"],
            price=data["price"],
            change=data["change"],
            tick=data["tick"],
            at=data["at"],
        )]

    return []


def _is_quote(value: Any) -> bool:
    if not isinstance(value, dict):
        return False
    return (
        value.get("type") == "quote"
        and isinstance(value.get("symbol"), str)
        and isinstance(value.get("price"), (int, float))
        and isinstance(value.get("change"), (int, float))
        and isinstance(value.get("tick"), (int, float))
        and isinstance(value.get("at"), str)
    )
