"""
StreamDescriptor type and invert_direction helper.

StreamDescriptor is the plain data shape that bridges the Agent SDK
(control plane) and the Stream SDK (data plane). It carries all the
information needed to open a stream connection.

invert_direction computes local_direction from agent_direction as
delivered in stream_started events.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Dict, Literal, Optional


StreamAffinity = Literal["dedicated", "shared"]


@dataclass
class StreamDescriptor:
    """Plain data shape carrying all information needed to open a stream connection.

    - agent_direction: what arrived on the wire in stream_started
    - local_direction: what the local client actually does
      (computed via invert_direction)
    - affinity: "dedicated" (per-task channel) or "shared" (cross-task
      broadcast). Required: the stream_started wire event carries it, and
      producer-side construction has it in scope from the card declaration.
      Consumer-side cleanup rules consult this field to decide whether to
      publish a stream_end marker on a shared channel (they must not).
    """

    task_id: str
    stream_id: str
    agent_name: str
    channel: str
    token: str
    agent_direction: str  # "outbound" | "inbound" | "bidirectional"
    local_direction: str  # "outbound" | "inbound" | "bidirectional"
    format: str  # "bytes" | "events"
    affinity: str  # "dedicated" | "shared"
    metadata: Optional[Dict[str, Any]] = None
    declared_stream: Optional[str] = None


def invert_direction(agent_direction: str) -> str:
    """Compute the local direction from the agent-facing direction
    delivered in stream_started.

    - agent outbound -> consumer inbound (consumer reads)
    - agent inbound -> consumer outbound (consumer writes)
    - agent bidirectional -> consumer bidirectional
    """
    if agent_direction == "outbound":
        return "inbound"
    elif agent_direction == "inbound":
        return "outbound"
    elif agent_direction == "bidirectional":
        return "bidirectional"
    else:
        raise ValueError(f"Unknown direction: {agent_direction}")
