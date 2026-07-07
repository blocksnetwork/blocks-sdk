"""
lifecycle consumer (Python) — mirrors the Node lifecycle example.

Drives one deterministic timeline that exercises all four task-lifecycle ops
against the lifecycle agent, in order:

  1. Submit a long-running ``pipe`` task; open the bidi control stream.
  2. Read a few progress ticks.
  3. pause  -> provider parks its work loop -> ticks stop.
  4. resume -> provider continues         -> ticks resume.
  5. cancel -> provider stops cooperatively -> terminal ``canceled``.
  6. Retry (request task): submit with failOnce -> terminal ``failed``, then
     resubmit with a FRESH idempotency_key -> ``completed``. Also resubmit with
     the SAME key to show the backend returns ``idempotent=True`` (no re-run).

Only the current consumer surface is used: cancel is ``client.cancel_task``;
pause/resume are app-level control messages on the stream; retry is a plain
resubmit. See README for the framework-vs-composed boundary.

Usage:
    python main.py

Environment variables:
    BLOCKS_API_KEY      -- Blocks API key (required)
    BLOCKS_CDM_URL      -- CDM config URL (optional, defaults to production CDN)
    BLOCKS_BACKEND_URL  -- Backend base URL (optional, overrides the CDM value)
"""

from __future__ import annotations

import json
import os
import sys
import threading
import time
from typing import List

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient, get_agent, fetch_cdm_config

AGENT_NAME = "lifecycle_python"

api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)


def main() -> None:
    cdm_url = os.environ.get("BLOCKS_CDM_URL")
    cdm = fetch_cdm_config(cdm_url)
    base_url = os.environ.get("BLOCKS_BACKEND_URL") or cdm.api.base_url

    # Pass api_key so a privately-registered (not-yet-published) agent
    # resolves — the registry returns 404 for private agents unauthenticated.
    entry = get_agent(AGENT_NAME, base_url=base_url, api_key=api_key)
    if not entry:
        print(f'Agent "{AGENT_NAME}" not found in the registry at {base_url}.', file=sys.stderr)
        sys.exit(1)
    billing_mode = entry.billing_mode or "free"
    keyset = "network" if billing_mode == "paid" else "playground"
    print(f"Registry says {AGENT_NAME} is billing_mode={billing_mode}; using {keyset} keyset.")

    client = TaskClient.create(billing_mode=billing_mode, api_key=api_key, cdm_url=cdm_url, base_url=base_url)

    run_pause_resume_cancel(client)
    run_retry(client)

    client.destroy()
    print("\n--- Done ---")


def run_pause_resume_cancel(client: TaskClient) -> None:
    """Steps 1-5: pause -> resume -> cancel over a bidi control stream."""
    print("\n=== pause / resume / cancel (pipe task) ===")
    session = client.send_message(
        agent_name=AGENT_NAME,
        task_kind="pipe",
        # Pipe tasks require a duration (max lifetime, minutes). We cancel long
        # before this. Note the card's `maxRunningTimeSec: 120` is the tighter
        # cap and fires first — keep any manual pause under ~20s so the demo
        # cancels before the card cap terminates the task.
        duration=5,
        request_parts=[{"partId": "params", "text": json.dumps({"ticks": 200})}],
    )
    print(f"Task created: {session.task_id}")

    # Log the backend cancel-ack so the built-in cancel path is visible.
    session.on_cancel_requested(
        lambda ev: print(f"[cancel] backend acknowledged cancel for {ev.task_id}")
    )

    ticks: List[int] = []
    ticks_lock = threading.Lock()

    stream_ref = session.wait_for_stream(timeout=30.0)
    stream = stream_ref.open()
    stream.on_error(lambda err: print(f"[control] stream error: {err}", file=sys.stderr))

    def read_ticks() -> None:
        try:
            for event in stream.events():
                tick = event.get("tick") if isinstance(event, dict) else None
                if isinstance(tick, int):
                    with ticks_lock:
                        ticks.append(tick)
                    print(f"[tick] {tick}")
        except Exception as exc:  # noqa: BLE001 — stream closed on cancel/teardown is expected
            print(f"[control] tick reader stopped: {exc}")

    reader = threading.Thread(target=read_ticks, name="lifecycle-tick-reader", daemon=True)
    reader.start()

    _wait_for_ticks(ticks, ticks_lock, 3, 8.0)
    print("--- pausing ---")
    stream.write({"ctrl": "pause"})

    # While paused, the tick count must hold steady — that is the proof the
    # work loop actually suspended (not just a status event).
    with ticks_lock:
        before_pause = len(ticks)
    time.sleep(2.5)
    with ticks_lock:
        during_pause = len(ticks)
    print(f"ticks during 2.5s pause: {during_pause - before_pause} (expected ~0)")

    print("--- resuming ---")
    stream.write({"ctrl": "resume"})
    _wait_for_ticks(ticks, ticks_lock, during_pause + 2, 8.0)
    print("ticks resumed after resume ✓")

    print("--- canceling ---")
    client.cancel_task(session.task_id)

    terminal = session.wait_for_terminal(timeout=30.0)
    print(f"--- pipe task {terminal.state} (expected: canceled) ---")

    try:
        stream.end()
    except Exception as exc:  # noqa: BLE001 — already ended by cancel/teardown is expected
        print(f"[control] stream.end() after cancel: {exc}", file=sys.stderr)
    reader.join(timeout=5.0)
    session.close()


def run_retry(client: TaskClient) -> None:
    """Step 6: fail-once -> retry with a fresh key -> same-key idempotent replay."""
    print("\n=== retry (request task) ===")

    # Per-run suffix so each script run submits FRESH idempotency keys — a
    # constant key would make run 2+ a terminal-idempotent replay (the stored
    # terminal is returned without re-running the handler), silently voiding the
    # retry demo. Attempt 3 deliberately reuses attempt 2's key to show dedupe.
    run_id = int(time.time())

    # Attempt 1: failOnce set -> terminal `failed`.
    fail_key = f"lifecycle-retry-{AGENT_NAME}-{run_id}-1"
    attempt1 = client.send_message(
        agent_name=AGENT_NAME,
        task_kind="request",
        idempotency_key=fail_key,
        request_parts=[{"partId": "params", "text": json.dumps({"failOnce": True})}],
    )
    t1 = attempt1.wait_for_terminal(timeout=30.0)
    print(f"attempt 1 (failOnce): {t1.state} (expected: failed)")
    attempt1.close()

    # Retry: the agent is stateless, so retry is a fresh submission with a NEW
    # idempotency_key and no failOnce flag -> `completed`.
    retry_key = f"lifecycle-retry-{AGENT_NAME}-{run_id}-2"
    attempt2 = client.send_message(
        agent_name=AGENT_NAME,
        task_kind="request",
        idempotency_key=retry_key,
        request_parts=[{"partId": "params", "text": json.dumps({"failOnce": False})}],
    )
    t2 = attempt2.wait_for_terminal(timeout=30.0)
    print(f"attempt 2 (retry, fresh key): {t2.state} (expected: completed)")
    attempt2.close()

    # Same-key resubmit: the backend dedupes and returns the prior result
    # instead of running the handler again.
    attempt3 = client.send_message(
        agent_name=AGENT_NAME,
        task_kind="request",
        idempotency_key=retry_key,
        request_parts=[{"partId": "params", "text": json.dumps({"failOnce": False})}],
    )
    print(f"attempt 3 (same key {retry_key}): idempotent={attempt3.idempotent is True} (expected: true)")
    attempt3.close()


def _wait_for_ticks(ticks: List[int], lock: threading.Lock, target: int, timeout: float) -> None:
    """Block until ``len(ticks) >= target`` or raise on timeout."""
    started = time.monotonic()
    while True:
        with lock:
            count = len(ticks)
        if count >= target:
            return
        if time.monotonic() - started > timeout:
            raise TimeoutError(f"Timed out waiting for {target} ticks (saw {count})")
        time.sleep(0.1)


if __name__ == "__main__":
    main()
