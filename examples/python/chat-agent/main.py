"""chat-agent consumer (Python).

An interactive multi-turn chat REPL against the chat-agent. You type a message,
the agent replies, and the earlier context is still there on the next message.
Each turn is a separate ``request`` task; the consumer reads the
``conversationId`` from the first turn's artifact and threads it into every
following turn so the agent can recall earlier context.

The wire protocol has no conversation field, so the id is carried inside the
request part's JSON ``text`` and echoed back in the artifact.

Usage:
    python main.py                              # interactive REPL
    python main.py "I'm Sam" "what's my name?"  # scripted turns, then exit

In the REPL, type ``exit`` or ``quit`` (or press Ctrl-D) to end the conversation.

Environment variables:
    BLOCKS_API_KEY      -- Blocks API key (required; run `blocks login --write-env`)
    BLOCKS_BACKEND_URL  -- backend base URL (optional; defaults to CDM config)
    BLOCKS_CDM_URL      -- CDM config URL (optional)
"""

from __future__ import annotations

import json
import os
import sys
from typing import Optional, Tuple

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient, fetch_cdm_config
from blocks_network.agent_registry import get_agent

AGENT_NAME = "chat_agent_python"


def main() -> None:
    api_key = os.environ.get("BLOCKS_API_KEY")
    if not api_key:
        print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
        sys.exit(1)

    cdm_url = os.environ.get("BLOCKS_CDM_URL")
    base_url = os.environ.get("BLOCKS_BACKEND_URL") or fetch_cdm_config(cdm_url).api.base_url

    entry = get_agent(AGENT_NAME, base_url=base_url, api_key=api_key)
    if entry is None:
        print(
            f"Agent '{AGENT_NAME}' not found at {base_url}. "
            "Run 'blocks register' in this folder first.",
            file=sys.stderr,
        )
        sys.exit(1)
    billing_mode = entry.billing_mode or "free"

    client = TaskClient.create(
        billing_mode=billing_mode,
        api_key=api_key,
        base_url=base_url,
        cdm_url=cdm_url,
    )

    def send_turn(text: str, conversation_id: Optional[str]) -> Tuple[str, str, int]:
        message = {"text": text}
        if conversation_id:
            message["conversationId"] = conversation_id

        session = client.send_message(
            agent_name=AGENT_NAME,
            request_parts=[
                {
                    "partId": "message",
                    "text": json.dumps(message),
                    "contentType": "application/json",
                }
            ],
        )

        try:
            session.wait_for_terminal(timeout=30)
        except TimeoutError:
            session.close()
            raise RuntimeError("Timed out waiting for terminal event.")

        refs = session.list_artifacts()
        if not refs:
            session.close()
            raise RuntimeError("Agent returned no artifact.")
        downloaded = session.download_artifact(refs[0])
        parsed = json.loads(downloaded.data.decode("utf-8"))
        session.close()
        return parsed["reply"], parsed["conversationId"], parsed["turn"]

    conversation_id: Optional[str] = None
    turns_sent = 0

    def say(text: str) -> None:
        nonlocal conversation_id, turns_sent
        reply, conversation_id, turn = send_turn(text, conversation_id)
        turns_sent = turn
        print(f"[agent] {reply}")
        print(f"        (conversationId={conversation_id}, turn={turn})")

    def run_scripted(turns: list[str]) -> None:
        for text in turns:
            print(f"\n[you]   {text}")
            say(text)

    def run_interactive() -> None:
        print("Interactive chat with the agent. Type 'exit' or 'quit' (or Ctrl-D) to end.\n")
        while True:
            try:
                text = input("[you]   ")
            except EOFError:
                break
            if text.strip() == "":
                continue
            if text.strip().lower() in ("exit", "quit"):
                break
            say(text)
            print("")

    args = sys.argv[1:]
    try:
        if args:
            run_scripted(args)
        else:
            run_interactive()
    finally:
        client.destroy()

    if turns_sent > 1:
        print("\nConversation complete. The agent recalled context from earlier turns.")
    else:
        print("\nConversation complete.")


if __name__ == "__main__":
    main()
