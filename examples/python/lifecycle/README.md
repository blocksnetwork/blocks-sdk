# Lifecycle (Python)

Canonical example for **task lifecycle**: cancel, pause, resume, and retry.
The point of this example is *honesty about the boundary* — which ops the
framework backs as first-class commands, and which a provider (or consumer)
**composes** from exposed primitives.

**Category:** Canonical -- task lifecycle (cancel / pause / resume / retry)

## Framework built-in vs. provider-composed

| Op | Who backs it | Mechanism in this example |
| --- | --- | --- |
| **cancel** | Framework (built-in) | `client.cancel_task(task_id)` sets `ctx.cancel_event`; the handler's work loop checks `ctx.is_cancelled` each iteration and stops → terminal `canceled`. `session.on_cancel_requested()` logs the backend ack. |
| **pause / resume** | Provider (composed) | The framework's `pause_task`/`resume_task` publish a `status` event **only** — the handler keeps running, so they do not suspend work. This example builds *real* suspension: the consumer writes `{ "ctrl": "pause" \| "resume" }` on a **bidirectional** stream and the handler parks its work loop on an app-level `paused` flag. |
| **retry** | Consumer (composed) | Task state is in-memory only (SDK_CONTRACT §17), so there is no resume-in-place. Retry is a consumer **resubmit** with a fresh `idempotency_key`. The handler is idempotent; a `failOnce` flag lets the consumer script a deterministic `failed` → `completed`. |

No op is faked and none is silently dropped. Where the framework only emits a
status event (pause/resume) or is a provider no-op (retry), the example builds
the real behavior at the application level and says so.

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login --write-env` to generate)

## Install

```bash
cd examples/python/lifecycle
pip install -e ".[consumer]"
```

## Run the agent

```bash
blocks register   # register privately (recommended first step)
blocks run
```

## Run the consumer

In a second terminal:

```bash
python main.py
```

The consumer scripts one deterministic timeline:

1. Submit a long-running `pipe` task and open the bidi control stream.
2. Read a few progress ticks.
3. **pause** → the handler parks its loop → ticks stop (the consumer prints
   the tick delta during the pause, expected ~0).
4. **resume** → the handler continues → ticks resume.
5. **cancel** → the handler stops cooperatively → terminal `canceled`.
6. **retry** (request task): submit with `failOnce` → `failed`; resubmit with a
   fresh `idempotency_key` → `completed`; resubmit with the *same* key →
   `idempotent=True` (no re-run).

## SDK concepts demonstrated

**Provider (`handler.py`)**

- `pipe` vs `request` task kinds and their terminal semantics: a pipe task does
  **not** auto-terminal on handler return (it runs until cancel/duration); a
  request task auto-`completed` on return and `failed` on raise.
- Cooperative cancel via `ctx.is_cancelled` checked each iteration
  (`ctx.cancel_event.wait()` makes the tick sleep interruptible).
- A **bidirectional** control stream (`ctx.create_stream(direction="bidirectional")`)
  read on a background thread so the work loop never blocks on input.
- An app-level `paused` flag the work loop parks on — real work-suspension.
- Idempotent handler logic (stateless `failOnce`) so a retry is safe to run.

**Consumer (`main.py`)**

- `TaskClient.create()`, `send_message(task_kind=..., duration=..., idempotency_key=...)`.
- `client.cancel_task(task_id)` and `session.on_cancel_requested()`.
- Writing control messages on the stream: `stream.write({"ctrl": "pause"})`.
- `session.wait_for_terminal()`, `session.idempotent` for same-key dedupe.
- Blocking stream iterators are read on daemon threads.

## Why pause/resume/retry are composed, not built-in

These are **not** blocking SDK gaps — they are a
framework-provides-X / provider-composes-Y distinction, and the boundary is the
teaching content. A possible (non-blocking) future improvement is for the
framework to expose a first-class pause gate and a real retry; the example does
not require it.

## What to edit

- Change `ticks` or the tick cadence (`TICK_SECONDS`) in `handler.py`.
- Add more control verbs to the inbound schema in `agent-card.json` and a
  matching branch in the handler's control reader.
