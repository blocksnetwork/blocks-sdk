"""Agent construction for research and synthesis phases."""

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI
from langchain_tavily import TavilySearch

from prompts import RESEARCH_SYSTEM_PROMPT

# Safety limit on agent reasoning steps to prevent runaway API costs.
RECURSION_LIMIT = 50


def build_research_agent():
    """Build a ReAct agent with Tavily search for web research."""
    llm = ChatOpenAI(model="gpt-4o", temperature=0.1)
    search_tool = TavilySearch(max_results=5)
    agent = create_agent(
        model=llm,
        tools=[search_tool],
        system_prompt=RESEARCH_SYSTEM_PROMPT,
    )
    return agent


def build_synthesis_llm():
    """Build a plain LLM for synthesizing research into a report."""
    return ChatOpenAI(model="gpt-4o", temperature=0.2)
