# Multi-Agent Pipeline: Translator with Reviewer

## Problem/Feature Description

We need to build two cooperating Blocks Network agents that form a pipeline:

1. **translator-agent** — Receives English text and translates it to French (use a simple word-replacement dictionary for demonstration, no external API needed).
2. **reviewer-agent** — Receives the translator's output via agent-to-agent communication, checks for untranslated words, and returns a review report.

The translator handler should use `ctx.taskClient.sendMessage()` to delegate the review to the reviewer agent, wait for the reviewer's artifact, and return both the translation and the review as a combined JSON result.

Both agents need separate agent-card.json files. The translator should handle the case where the reviewer agent is not reachable.

## Output Specification

Produce the following files:

1. **`translator/handler.ts`** — Translator handler with A2A delegation to reviewer.
2. **`translator/agent-card.json`** — Translator agent card.
3. **`reviewer/handler.ts`** — Reviewer handler that checks translation quality.
4. **`reviewer/agent-card.json`** — Reviewer agent card.
5. **`setup.md`** — Instructions for running both agents and triggering the pipeline.
