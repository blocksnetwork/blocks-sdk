"""advanced-stream consumer.

Submit a pipe task, then read all three declared streams concurrently,
branching on each stream's declared name and format.

Because Python's stream iterators are blocking, each stream is read on its own
daemon thread. Dedicated streams end naturally when the agent calls
stream.end(); the shared ``broadcast`` stream has no per-task stream_end, so
its reader only unwinds when we close the session after terminal.

Usage:
    python main.py           # default 5 ticks
    python main.py 10        # request 10 ticks

Environment variables:
    BLOCKS_API_KEY  -- Blocks API key (required)
    BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
"""

from __future__ import annotations

import json
import os
import sys
import threading

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient, get_agent, fetch_cdm_config

AGENT_NAME = "advanced_stream"

api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)


def main() -> None:
    ticks_arg = sys.argv[1] if len(sys.argv) > 1 else None
    request_parts = (
        [{"partId": "params", "text": json.dumps({"ticks": int(ticks_arg)})}]
        if ticks_arg and ticks_arg.isdigit()
        else []
    )

    # Resolve baseUrl + the agent's registered billing mode so we pick the
    # matching keyset (free -> playground, paid -> network).
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

    print(f"Submitting pipe task to {AGENT_NAME}{f' (ticks={ticks_arg})' if request_parts else ''}...")
    # Pipe tasks require a duration (max lifetime in minutes). The agent ends
    # its streams after `ticks`, well before this cap.
    session = client.send_message(agent_name=AGENT_NAME, task_kind="pipe", duration=1, request_parts=request_parts)
    print(f"Task created: {session.task_id}")
    print("---")

    seen: set[str] = set()
    threads: list[threading.Thread] = []

    def on_stream(stream_ref) -> None:
        desc = stream_ref.descriptor
        name = desc.declared_stream or desc.stream_id
        if name in seen:
            return
        seen.add(name)
        print(f"Stream discovered: {name} (format={desc.format}, affinity={desc.affinity})")
        t = threading.Thread(target=_read_stream, args=(name, desc.format, stream_ref), daemon=True)
        t.start()
        threads.append(t)

    session.on_stream(on_stream)

    terminal = session.wait_for_terminal(timeout=60.0)
    print(f"--- Task {terminal.state} ---")

    # Closing the session unsubscribes and unwinds any still-open readers
    # (notably the shared broadcast stream, which has no per-task stream_end).
    session.close()
    client.destroy()
    print("--- Done ---")


def _read_stream(name: str, fmt: str, stream_ref) -> None:
    stream = stream_ref.open()
    stream.on_error(lambda err: print(f"[{name}] stream error: {err}", file=sys.stderr))

    try:
        if fmt == "bytes":
            for chunk in stream.bytes():
                sys.stdout.write(f"[{name}] {chunk.decode('utf-8', errors='replace')}")
                sys.stdout.flush()
        else:
            for event in stream.events():
                print(f"[{name}] {json.dumps(event)}")
        print(f"[{name}] ended")
    except Exception as err:
        # Expected on session teardown: closing the session unwinds the shared
        # broadcast reader (which has no per-task stream_end) mid-iteration.
        print(f"[{name}] reader closed: {err}", file=sys.stderr)


if __name__ == "__main__":
    main()
