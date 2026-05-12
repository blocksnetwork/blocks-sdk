"""
Orchestrator handler -- fans out to echo and adder agents in parallel,
subscribes to real-time results, and compiles a summary.

Mirrors the Node orchestrator example handler.
"""

from __future__ import annotations

import base64
import json
import math
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any, Dict, List, Optional

from blocks_network.task_client import SendMessageParams, TaskClient, TaskEventCallbacks
from blocks_network.types import StartTaskMessage, TaskContext

# Client-side timeout for each sub-task.  Must be less than the orchestrator's
# own maxRunningTimeSec (60 s in agent-card.json) to leave time for result
# collection and response assembly.
SUB_TASK_TIMEOUT_SEC = 30


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    """Orchestrator handler - dispatches to echo and adder, compiles results."""
    # ctx.task_client lets an agent dispatch sub-tasks to other agents on the network
    if not ctx or not ctx.task_client:
        raise RuntimeError("TaskClient not available — handler requires TaskContext")

    input_data = _parse_input(task)
    task_client: TaskClient = ctx.task_client

    ctx.report_status("Dispatching sub-tasks...")

    owner_id = task.owner_id

    # Fan out sub-tasks in parallel using threads
    with ThreadPoolExecutor(max_workers=2) as pool:
        echo_future = pool.submit(
            _execute_sub_task,
            task_client,
            "echo-python",
            [{"partId": "text", "text": input_data["echo_text"]}],
            owner_id,
        )
        adder_future = pool.submit(
            _execute_sub_task,
            task_client,
            "adder",
            [{"partId": "numbers", "text": json.dumps({"kind": "math_add", "a": input_data["a"], "b": input_data["b"]})}],
            owner_id,
        )

    echo_result = echo_future.result()
    adder_result = adder_future.result()

    if ctx:
        ctx.report_status("Compiling results...")

    output = {
        "ok": echo_result["status"] == "completed" and adder_result["status"] == "completed",
        "echo": echo_result,
        "adder": adder_result,
        "summary": f"Echo: {echo_result['status']}, Adder: {adder_result['status']}",
    }

    return {
        "artifacts": [{"data": json.dumps(output, indent=2), "mimeType": "application/json"}],
    }


# ---------------------------------------------------------------------------
# Sub-task execution helper
# ---------------------------------------------------------------------------


def _decode_artifact(ref: Any) -> Any:
    """Decode an artifact reference.

    Inline artifacts carry base64-encoded data; decode to string or parsed JSON.
    File artifacts without downloadable data are returned as-is.
    """
    if ref is None or not isinstance(ref, dict):
        return ref
    if ref.get("kind") == "inline" and isinstance(ref.get("data"), str):
        text = base64.b64decode(ref["data"]).decode("utf-8")
        try:
            return json.loads(text)
        except (json.JSONDecodeError, ValueError):
            return text
    return ref


def _execute_sub_task(
    task_client: TaskClient,
    agent_name: str,
    request_parts: List[Any],
    owner_id: str,
) -> Dict[str, Any]:
    """Send a sub-task, subscribe to results, and block until completion."""
    try:
        # send_message creates a new task targeting the named agent and returns a session for events
        sent = task_client.send_message(
            SendMessageParams(
                agent_name=agent_name,
                request_parts=request_parts,
                owner_id=owner_id,
            )
        )
    except Exception as err:
        return {"status": "failed", "error": str(err) or "sendMessage failed"}

    # Use threading primitives to block until the sub-task finishes
    done_event = threading.Event()
    result: Dict[str, Any] = {}
    artifact_holder: List[Any] = [None]

    def _finish(outcome: Dict[str, Any]) -> None:
        if result:  # already settled
            return
        result.update(outcome)
        done_event.set()

    def _on_artifact(event: Dict[str, Any]) -> None:
        artifact_holder[0] = _decode_artifact(
            event.get("artifactRef") or event.get("artifact")
        )

    def _on_terminal(event: Dict[str, Any]) -> None:
        if event.get("state") == "completed":
            _finish({"status": "completed", "artifact": artifact_holder[0]})
        else:
            _finish({
                "status": "failed",
                "error": event.get("error") or event.get("state") or "unknown",
            })

    # on_artifact/on_terminal subscribe to real-time events on the task's control channel
    sent.on_artifact(_on_artifact)
    sent.on_terminal(_on_terminal)

    # Guard against race: the sub-task may complete before the subscription
    # is active. A single getTask poll right after subscribing covers this gap.
    try:
        info = task_client.get_task(sent.task_id)
        state = info.state
        if state in ("completed", "failed", "canceled"):
            # If completed but the artifact event hasn't arrived yet via
            # subscription, try to extract it from the task record.
            artifact = artifact_holder[0]
            if state == "completed" and artifact is None:
                arts = info.extra.get("artifacts")
                if isinstance(arts, list) and len(arts) > 0:
                    artifact = _decode_artifact(arts[-1])
            if state == "completed":
                _finish({"status": "completed", "artifact": artifact})
            else:
                _finish({"status": "failed", "error": state})
    except Exception:
        pass  # poll failed; rely on real-time subscription

    # Block until done or timeout
    done_event.wait(timeout=SUB_TASK_TIMEOUT_SEC)
    sent.close()

    if not result:
        return {
            "status": "timeout",
            "error": f"{agent_name} timed out after {SUB_TASK_TIMEOUT_SEC}s",
        }

    return result


# ---------------------------------------------------------------------------
# Input parsing
# ---------------------------------------------------------------------------


def _parse_input(task: StartTaskMessage) -> Dict[str, Any]:
    """Parse orchestrator input with defaults."""
    defaults: Dict[str, Any] = {
        "echo_text": "Hello from Orchestrator!",
        "a": 3,
        "b": 4,
    }

    for part in task.request_parts or []:
        content = _parse_part_content(part)
        if isinstance(content.get("echoText"), str):
            defaults["echo_text"] = content["echoText"]
        if isinstance(content.get("a"), (int, float)) and math.isfinite(content["a"]):
            defaults["a"] = content["a"]
        if isinstance(content.get("b"), (int, float)) and math.isfinite(content["b"]):
            defaults["b"] = content["b"]

    return defaults


def _parse_part_content(part: Any) -> Dict[str, Any]:
    """Extract structured content from a RequestPart or raw dict.

    The frontend serializes structured content into the ``text`` field as
    JSON.  This helper parses it back into a dict.
    """
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
        return {"text": text}
    if hasattr(part, "extra") and isinstance(part.extra, dict):
        return part.extra
    if isinstance(part, dict):
        return part
    return {}
