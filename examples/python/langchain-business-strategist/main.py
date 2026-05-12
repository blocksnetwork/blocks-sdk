"""Blocks Network agent instance — runs the competitive analysis pipeline on demand via PubNub."""

import asyncio
import os
import signal
import sys
import threading
from typing import Dict, Optional, Callable

from dotenv import load_dotenv

# Load .env BEFORE importing blocks_network — its config module reads env vars at
# import time, so the values must already be in os.environ.
load_dotenv()

from agent import RECURSION_LIMIT, build_research_agent, build_synthesis_llm
from prompts import SYNTHESIS_SYSTEM_PROMPT, get_research_queries
from report import REPORT_TEMPLATE, save_report

from blocks_network.agent_instance import start_agent_instance
from blocks_network.types import AgentInstanceOptions, StartTaskMessage, TaskContext


def validate_env():
    """Ensure required API keys and PubNub keys are set."""
    missing = []
    for key in ("OPENAI_API_KEY", "TAVILY_API_KEY", "PUBNUB_PUBLISH_KEY", "PUBNUB_SUBSCRIBE_KEY"):
        if not os.environ.get(key):
            missing.append(key)
    if missing:
        print(f"Error: Missing required environment variables: {', '.join(missing)}")
        print("Copy .env.example to .env and fill in your API keys.")
        sys.exit(1)


async def _research_one(agent, query, index, total):
    """Run a single research query and return (query_id, content, success)."""
    label = query["label"]
    print(f"\n[{index}/{total}] Researching: {label}...")
    try:
        response = await agent.ainvoke(
            {"messages": [{"role": "user", "content": query["query"]}]},
            config={"recursion_limit": RECURSION_LIMIT},
        )
        last_message = response["messages"][-1]
        content = last_message.content if hasattr(last_message, "content") else str(last_message)
        print(f"  ✓ {label} complete")
        return query["id"], content, True
    except Exception as e:
        print(f"  ✗ {label} failed: {e}")
        return query["id"], f"Research failed: {e}", False


async def run_research(agent, queries):
    """Run all research queries concurrently, returning results."""
    tasks = [
        _research_one(agent, q, i, len(queries))
        for i, q in enumerate(queries, 1)
    ]
    outcomes = await asyncio.gather(*tasks)
    results = {qid: content for qid, content, _ in outcomes}
    succeeded = sum(1 for _, _, ok in outcomes if ok)
    return results, succeeded


def run_synthesis(llm, research_results):
    """Synthesize research results into a structured report."""
    print("\nSynthesizing findings into report...")

    combined_research = "\n\n---\n\n".join(
        f"### {query_id}\n\n{content}"
        for query_id, content in research_results.items()
    )

    synthesis_prompt = f"""Based on the following research findings, produce a comprehensive competitive analysis report.

Use this markdown template structure for the report:

{REPORT_TEMPLATE}

Fill in each section placeholder with substantive analysis based on the research below. Replace the placeholder variables (like {{executive_summary}}, {{pubnub_profile}}, etc.) with actual content. For comparison sections, use markdown tables.

---

RESEARCH FINDINGS:

{combined_research}"""

    response = llm.invoke([
        {"role": "system", "content": SYNTHESIS_SYSTEM_PROMPT},
        {"role": "user", "content": synthesis_prompt},
    ])

    return response.content


async def run_pipeline(report_status: Optional[Callable[[str], None]] = None):
    """Orchestrate the full research + synthesis pipeline.

    Calls ``report_status(msg)`` at phase boundaries so the SDK can relay
    progress heartbeats to observers.  Returns the markdown report string.
    """
    if report_status:
        report_status("Starting competitive analysis pipeline")

    # Phase 1: Research (concurrent)
    print("\n--- Phase 1: Research ---")
    if report_status:
        report_status("Phase 1: Running research queries")
    agent = build_research_agent()
    queries = get_research_queries()
    research_results, successful = await run_research(agent, queries)

    print(f"\nResearch complete: {successful}/{len(queries)} queries succeeded")

    if successful == 0:
        raise RuntimeError("All research queries failed. Cannot generate report.")

    # Phase 2: Synthesis
    print("\n--- Phase 2: Synthesis ---")
    if report_status:
        report_status("Phase 2: Synthesizing report")
    llm = build_synthesis_llm()
    report_content = run_synthesis(llm, research_results)

    if report_status:
        report_status("Pipeline complete")

    return report_content


def handler(task: StartTaskMessage, ctx: TaskContext) -> Dict:
    """Blocks handler — runs the competitive analysis pipeline for a task."""
    print(f"\n{'=' * 60}")
    print(f"  Task {task.task_id}: LangChain Business Strategy Agent")
    print(f"  PubNub Competitive Analysis")
    print(f"{'=' * 60}")

    report_content = asyncio.run(run_pipeline(report_status=ctx.report_status))

    # Best-effort save to disk for local inspection
    try:
        filepath = save_report(report_content)
        print(f"\n  Report also saved locally: {filepath}")
    except Exception as e:
        print(f"\n  Could not save report locally: {e}")

    return {
        "artifact": report_content,
        "mimeType": "text/markdown",
        "fileName": "competitive_analysis_report.md",
    }


def main():
    validate_env()

    agent_name = os.environ.get("AGENT_NAME", "langchain-business-strategist")

    print(f"Starting agent instance (agent_name: {agent_name})...")

    result = start_agent_instance(AgentInstanceOptions(
        agent_name=agent_name,
        handler=handler,
        max_threads=1,
    ))

    print(f"Agent instance started: {result['instance_id']}")
    print(f"Listening for tasks on agent.{agent_name}.control")
    print("Press Ctrl+C to stop.")

    # Block until SIGINT/SIGTERM
    stop_event = threading.Event()

    def _shutdown(signum, frame):
        print("\nShutting down...")
        stop_event.set()

    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)

    stop_event.wait()

    result["stop"]()
    print("Agent instance stopped.")


if __name__ == "__main__":
    main()
