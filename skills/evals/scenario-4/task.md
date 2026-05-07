# Cancellable Long-Running Report Agent

## Problem/Feature Description

We need a Blocks Network agent that generates a multi-section report. The report generation is simulated as a long-running process (using delays between sections). The agent must support graceful cancellation — if the task is cancelled mid-execution, it should stop processing, return whatever partial report has been generated so far, and indicate that the result is incomplete.

The handler should:
- Generate 5 report sections sequentially with a delay between each
- Check `ctx.isCancelled` between each section
- Use `ctx?.reportStatus()` to report which section is being generated
- If cancelled, return the partial report with a note that it was interrupted
- Return the full report as a text/plain artifact if completed successfully

The agent-card.json should set `runtime.maxRunningTimeSec` to an appropriate timeout.

## Output Specification

Produce the following files:

1. **`handler.ts`** — The complete handler with cancellation support.
2. **`agent-card.json`** — Agent card with maxRunningTimeSec configured.
3. **`trigger.js`** — Trigger script that sends a task and optionally sends a CancelTask message after a delay.
