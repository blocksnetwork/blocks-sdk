"""
Stock-sim handler.

Pipe agent that streams random stock price updates once per second
for the requested symbols. Runs until the local duration timer fires
or CancelTask/TerminateTask arrives.
"""

from __future__ import annotations

import json
import math
import random
import time
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from blocks_network.types import StartTaskMessage, TaskContext

DEFAULT_SYMBOLS = ["AAPL", "MSFT", "NVDA"]


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    log = lambda msg: print(f"[stock-sim] {msg}")

    if ctx is None:
        return {
            "artifacts": [{"data": json.dumps({"error": "TaskContext is required for streaming"}), "mimeType": "application/json"}],
        }

    if task.task_kind and task.task_kind != "pipe":
        raise RuntimeError("stock-sim only supports pipe tasks")

    log(f"Task {task.task_id} received from {task.owner_id}")

    symbols = _parse_symbols(task.request_parts)
    duration_minutes = _normalize_duration(task.duration)

    log(f"Symbols: {', '.join(symbols)}  Duration: {duration_minutes}m")

    stream = ctx.create_stream(format="events")
    log(f"Stream created: {stream.channel}")

    last_quotes: Dict[str, Dict[str, Any]] = {}
    updates_emitted = 0
    tick = 0

    ctx.report_status(
        f"Streaming {', '.join(symbols)} for {duration_minutes} "
        f"minute{'s' if duration_minutes != 1 else ''}..."
    )

    try:
        while not ctx.is_cancelled:
            tick += 1

            for symbol in symbols:
                quote = _build_quote(symbol, last_quotes.get(symbol, {}).get("price"), tick)
                last_quotes[symbol] = quote
                updates_emitted += 1
                stream.write(quote)

            if tick % 10 == 0:
                log(f"Tick {tick}: {updates_emitted} quotes sent")

            _sleep_or_cancel(1.0, ctx)
    except (_CancelledError, RuntimeError):
        # RuntimeError from stream.write() on an ended stream means the
        # SDK closed the stream (e.g. SIGINT) before the loop detected
        # cancellation — treat it the same as a cancel signal.
        pass

    log(f"Ending stream ({updates_emitted} quotes sent across {tick} ticks)")
    try:
        stream.end()
    except RuntimeError:
        pass  # Already ended by SDK shutdown
    log("Stream ended")

    completion_reason = (
        "duration_expired" if ctx.is_expired
        else "canceled" if ctx.is_cancelled
        else "stopped"
    )
    log(f"Task complete: {completion_reason}")

    ctx.report_status(
        "Streaming complete (duration expired)" if ctx.is_expired else "Streaming stopped"
    )

    return {
        "artifacts": [{
            "data": json.dumps(
                {
                    "symbols": symbols,
                    "requestedDurationMinutes": duration_minutes,
                    "updatesEmitted": updates_emitted,
                    "completionReason": completion_reason,
                    "lastQuotes": last_quotes,
                },
                indent=2,
            ),
            "mimeType": "application/json",
        }],
    }


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class _CancelledError(Exception):
    pass


def _sleep_or_cancel(seconds: float, ctx: TaskContext) -> None:
    """Sleep in small increments, raising _CancelledError if cancelled."""
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if ctx.is_cancelled:
            raise _CancelledError()
        time.sleep(min(0.05, end - time.monotonic()))


def _parse_symbols(parts: Optional[List[Any]]) -> List[str]:
    if not parts:
        return list(DEFAULT_SYMBOLS)

    candidates: List[str] = []
    for part in parts:
        if isinstance(part, str):
            candidates.append(part)
            continue
        content = _parse_part_content(part)
        if isinstance(content.get("text"), str):
            candidates.append(content["text"])
        if isinstance(content.get("symbols"), str):
            candidates.append(content["symbols"])
        if isinstance(content.get("symbols"), list):
            for s in content["symbols"]:
                if isinstance(s, str):
                    candidates.append(s)

    symbols = _normalize_symbols(",".join(candidates))
    return symbols if symbols else list(DEFAULT_SYMBOLS)


def _parse_part_content(part: Any) -> Dict[str, Any]:
    """Extract structured content from a RequestPart or raw dict.

    The frontend serializes structured content into the ``text`` field as
    JSON.  This helper parses it back into a dict.  When ``text`` is plain
    text (not JSON), the returned dict preserves a ``text`` key so callers
    can still read it.
    """
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
        # text is plain string -- preserve it in the returned dict
        return {"text": text}
    if hasattr(part, "extra") and isinstance(part.extra, dict):
        return part.extra
    if isinstance(part, dict):
        return part
    return {}


def _normalize_symbols(raw: str) -> List[str]:
    seen: set = set()
    result: List[str] = []
    for s in raw.split(","):
        s = s.strip().upper()
        if s and s not in seen:
            seen.add(s)
            result.append(s)
    return result


def _normalize_duration(value: Optional[float]) -> int:
    if value is not None and isinstance(value, (int, float)) and math.isfinite(value) and value > 0:
        return max(1, int(value))
    return 1


def _build_quote(
    symbol: str, previous_price: Optional[float], tick: int
) -> Dict[str, Any]:
    start_price = previous_price if previous_price is not None else _base_price(symbol)
    next_price = max(1.0, start_price + _random_delta(start_price))
    rounded = round(next_price, 2)
    prev_rounded = previous_price if previous_price is not None else rounded
    return {
        "type": "quote",
        "symbol": symbol,
        "price": rounded,
        "change": round(rounded - prev_rounded, 2),
        "tick": tick,
        "at": datetime.now(timezone.utc).isoformat(),
    }


def _base_price(symbol: str) -> float:
    h = 0
    for ch in symbol:
        h = ((h << 5) - h + ord(ch)) & 0xFFFFFFFF
    if h > 0x7FFFFFFF:
        h -= 0x100000000
    return 50 + abs(h % 250)


def _random_delta(price: float) -> float:
    max_move = max(0.5, price * 0.015)
    return (random.random() * 2 - 1) * max_move
