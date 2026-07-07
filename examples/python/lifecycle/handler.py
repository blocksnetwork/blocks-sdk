"""
lifecycle handler (Python) — mirrors the Node lifecycle example.

Teaches the boundary between task-lifecycle ops the framework backs
first-class and ops a provider composes from primitives:

  - cancel  (built-in)           cooperative: the SDK sets ctx.cancel_event
                                 and the loop stops -> terminal ``canceled``.
  - pause   (provider-composed)  NOT a handler hook — the framework's
  - resume  (provider-composed)  PauseTask/ResumeTask publish a status event
                                 only and never reach handler code. So we
                                 build real work-suspension: read
                                 { "ctrl": "pause" | "resume" } on a bidi
                                 control stream and park the work loop.
  - retry   (consumer-composed)  NOT done here — task state is in-memory only
                                 (SDK_CONTRACT §17), so retry is a consumer
                                 resubmit. This handler stays idempotent and
                                 honors a ``failOnce`` flag so the consumer can
                                 drive a failed->completed retry.

Task-kind split:
  - pipe:    long-running work loop with pause/resume/cancel (no auto-terminal).
  - request: single-shot; raises when ``failOnce`` is set (drives the retry demo).
"""

from __future__ import annotations

import json
import math
import threading
from typing import Any, Dict, List, Optional

from blocks_network.types import StartTaskMessage, TaskContext

TICK_SECONDS = 0.5
DEFAULT_TICKS = 20
MAX_TICKS = 200


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    params = _parse_params(task.request_parts)
    is_pipe = not task.task_kind or task.task_kind == "pipe"
    if not is_pipe:
        return _run_request(task, params)
    return _run_pipe(task, params, ctx)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _log(msg: str) -> None:
    print(f"[lifecycle] {msg}")


def _artifact(payload: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "artifacts": [{
            "data": json.dumps(payload, indent=2),
            "mimeType": "application/json",
        }],
    }


def _run_request(task: StartTaskMessage, params: Dict[str, Any]) -> Dict[str, Any]:
    """Request path: stateless fail-once retry demo.

    The consumer sends failOnce on attempt 1 (-> terminal ``failed``) and
    resubmits with a fresh idempotencyKey and no flag (-> ``completed``). No
    persisted "have I run before?" state is needed.
    """
    if params["fail_once"]:
        _log(f"Task {task.task_id}: failOnce set — raising to produce a failed terminal")
        raise RuntimeError("lifecycle: simulated first-attempt failure (failOnce)")
    _log(f"Task {task.task_id}: request completed")
    return _artifact({"kind": "request", "completionReason": "completed"})


def _run_pipe(
    task: StartTaskMessage, params: Dict[str, Any], ctx: Optional[TaskContext]
) -> Dict[str, Any]:
    """Pipe path: pause / resume / cancel work loop over a bidi control stream."""
    # Raise (not return) on a missing stream: a pipe handler's voluntary return
    # publishes NO terminal, so returning an error artifact would leave the task
    # lingering until duration expiry and then terminal as ``completed``. A raise
    # publishes an immediate, correct ``failed`` — matching the request path.
    if ctx is None or not ctx.has_stream:
        raise RuntimeError("lifecycle pipe task requires a negotiated stream")

    _log(f"Task {task.task_id}: starting work loop (up to {params['ticks']} ticks)")
    control = ctx.create_stream(declared_stream="control", direction="bidirectional", format="events")

    # `paused` is the single source of truth the work loop parks on; `resumed`
    # records that we came back from a pause.
    state = {"paused": False, "resumed": False}
    reader = threading.Thread(
        target=_read_control, args=(control, ctx, state), name="lifecycle-control", daemon=True
    )
    reader.start()

    emitted = _work_loop(control, ctx, params, state)

    try:
        control.end()
    except RuntimeError:
        pass  # already ended by SDK shutdown
    reader.join(timeout=2.0)

    completion_reason = "canceled" if ctx.is_cancelled else "completed"
    _log(f"Work loop {completion_reason} ({emitted} ticks, paused={state['paused'] or state['resumed']})")
    ctx.report_status(completion_reason)

    return _artifact(
        {
            "kind": "pipe",
            "ticks": emitted,
            "paused": state["paused"],
            "resumed": state["resumed"],
            "completionReason": completion_reason,
        }
    )


def _read_control(control: Any, ctx: TaskContext, state: Dict[str, bool]) -> None:
    """Read control messages on a background thread so the work loop never
    blocks waiting on input."""
    try:
        for event in control.events():
            ctrl = event.get("ctrl") if isinstance(event, dict) else None
            if ctrl == "pause" and not state["paused"]:
                state["paused"] = True
                _log("control: pause — parking work loop")
                ctx.report_status("paused")
            elif ctrl == "resume" and state["paused"]:
                state["paused"] = False
                state["resumed"] = True
                _log("control: resume — continuing work loop")
                ctx.report_status("running")
    except Exception as exc:  # noqa: BLE001 — stream torn down on cancel/completion is expected
        _log(f"control reader stopped: {exc}")


def _work_loop(control: Any, ctx: TaskContext, params: Dict[str, Any], state: Dict[str, bool]) -> int:
    ctx.report_status("running")
    emitted = 0
    tick = 0
    while tick < params["ticks"] and not ctx.is_cancelled:
        # Park while paused. This is the real suspension the framework's
        # status-only PauseTask does not provide: no progress is emitted and
        # the tick counter does not advance until we resume (or cancel).
        while state["paused"] and not ctx.is_cancelled:
            _sleep_or_cancel(TICK_SECONDS, ctx)
        if ctx.is_cancelled:
            break

        tick += 1
        try:
            control.write({"tick": tick, "state": "running"})
        except RuntimeError:
            break  # stream ended by SDK shutdown
        emitted += 1
        _sleep_or_cancel(TICK_SECONDS, ctx)
    return emitted


def _sleep_or_cancel(seconds: float, ctx: TaskContext) -> None:
    """Sleep up to ``seconds``, returning early if cancellation is requested."""
    # cancel_event.wait() returns True as soon as the event is set, so a
    # cancel interrupts the sleep immediately rather than after the full tick.
    ctx.cancel_event.wait(timeout=seconds)


def _parse_params(parts: Optional[List[Any]]) -> Dict[str, Any]:
    ticks = DEFAULT_TICKS
    fail_once = False
    for part in parts or []:
        content = _parse_part_content(part)
        value = content.get("ticks")
        # `json.loads` accepts NaN/Infinity literals; guard so a non-finite
        # `ticks` falls back to the default instead of crashing `int()` (Node
        # guards with `Number.isFinite`).
        if isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value):
            ticks = min(MAX_TICKS, max(1, int(value)))
        flag = content.get("failOnce")
        if isinstance(flag, bool):
            fail_once = flag
    return {"ticks": ticks, "fail_once": fail_once}


def _parse_part_content(part: Any) -> Dict[str, Any]:
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
    if isinstance(part, dict):
        return part
    return {}
