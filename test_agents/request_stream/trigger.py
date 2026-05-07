"""Trigger a task on the request_stream agent and print the result."""

import base64
import json
import os
import threading

from dotenv import load_dotenv
load_dotenv()

from blocks_network import (
    SendMessageParams,
    TaskClient,
    create_pubnub_client,
    fetch_cdm_config,
)


def main():
    cdm_config = fetch_cdm_config()
    subscribe_key = cdm_config.playground.subscribe_key
    publish_key = cdm_config.playground.publish_key
    base_url = cdm_config.api.base_url
    auth_token = os.environ.get("BLOCKS_TOKEN") or None

    # Derive user_id from the BLOCKS_TOKEN JWT (sub claim)
    if auth_token:
        import json as _json
        user_id = _json.loads(
            base64.b64decode(auth_token.split(".")[1] + "==")
        )["sub"]
    else:
        user_id = "trigger-script"

    client = TaskClient(
        subscribe_key=subscribe_key,
        auth_token=auth_token,
        base_url=base_url,
        create_pubnub=lambda: create_pubnub_client(
            subscribe_key=subscribe_key,
            publish_key=publish_key,
            user_id=user_id,
        ),
    )

    session = client.send_message(SendMessageParams(
        agent_name="request_stream",
        owner_id=user_id,
        request_parts=[
            {
                "partId": "request",
                "text": json.dumps(
                    {"message": "Hello from trigger!", "seconds": 3}
                ),
            }
        ],
    ))

    print(f"Task created: {session.task_id}")

    done = threading.Event()

    def on_progress(event):
        print("[progress]", event.get("message", event.get("progress", "")))

    def on_artifact(event):
        ref = event.get("artifactRef", {})
        if ref.get("kind") == "inline" and ref.get("data"):
            text = base64.b64decode(ref["data"]).decode()
            print("[artifact]", text)
        elif ref.get("fileUrl"):
            import urllib.request
            with urllib.request.urlopen(ref["fileUrl"]) as resp:
                print("[artifact]", resp.read().decode())
        else:
            print("[artifact]", ref)

    def on_terminal(event):
        print("[done] Task complete")
        done.set()

    session.on_progress(on_progress)
    session.on_artifact(on_artifact)
    session.on_terminal(on_terminal)

    done.wait(timeout=60)
    session.close()
    client.destroy()


if __name__ == "__main__":
    main()
