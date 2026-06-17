import os

from .logging_utils import log_agent_instance_event


def is_profiling_enabled() -> bool:
    raw = os.environ.get("BLOCKS_PROFILE", "")
    return any(tok.strip() == "timing" for tok in raw.split(","))


def log_dispatch_timing(
    task_id: str,
    *,
    received_ms: int,
    running_ms: int,
    handler_ms: int,
) -> None:
    """Emit a single-clock dispatch timing line for one task.

    No-op unless BLOCKS_PROFILE contains the ``timing`` token. All marks come
    from the same process clock, so deltas are skew-free locally.

    Chronological order of the marks is: StartTask received -> ``running``
    event published -> user handler invoked. The runtime publishes ``running``
    before invoking the handler, so the phases are decomposed as
    received->running and running->handler (both non-negative by construction).
    """
    if not is_profiling_enabled():
        return
    log_agent_instance_event(
        "info",
        "dispatch timing",
        task_id=task_id,
        received_to_running_ms=running_ms - received_ms,
        running_to_handler_ms=handler_ms - running_ms,
        received_to_handler_ms=handler_ms - received_ms,
    )
