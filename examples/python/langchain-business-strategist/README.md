# LangChain Business Strategy Agent

A LangChain-powered agent that researches PubNub and its competitors (Ably, Pusher, Firebase) by searching the web, then synthesizes findings into a structured competitive analysis report.

## How It Works

1. **Research Phase** — A ReAct agent with Tavily search runs 6 focused queries:
   - 4 per-company queries (PubNub, Ably, Pusher, Firebase) covering news, products, and features
   - 1 cross-company pricing and feature comparison
   - 1 market trends query

2. **Synthesis Phase** — GPT-4o takes all research results and produces a structured markdown report with executive summary, company profiles, comparison tables, and strategic recommendations.

## Setup

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

Copy the environment template and fill in your API keys:

```bash
cp .env.example .env
```

You'll need:
- **OPENAI_API_KEY** — from [platform.openai.com](https://platform.openai.com/)
- **TAVILY_API_KEY** — from [tavily.com](https://tavily.com/)

## Usage

```bash
python main.py
```

The agent will print progress as it researches each topic, then generate a report at:

```
output/competitive_analysis_YYYY-MM-DD_HH-MM-SS.md
```

## Project Structure

```
├── main.py           # CLI entry point and orchestration
├── agent.py          # ReAct agent and synthesis LLM construction
├── prompts.py        # Company definitions, research queries, system prompts
├── report.py         # Markdown report template and file output
├── requirements.txt  # Python dependencies
├── .env.example      # API key template
└── output/           # Generated reports (gitignored)
```
