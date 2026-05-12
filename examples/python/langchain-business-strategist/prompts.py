"""Prompts, company definitions, and research queries for competitive analysis."""

COMPANIES = {
    "PubNub": "Real-time communication infrastructure provider offering pub/sub messaging, presence, and chat APIs for web and mobile applications.",
    "Ably": "Real-time messaging platform providing pub/sub, presence, and streaming APIs with a focus on reliability and global edge infrastructure.",
    "Pusher": "Real-time communication API provider known for Channels (pub/sub) and Beams (push notifications) products for web and mobile developers.",
    "Firebase": "Google's app development platform offering Realtime Database, Cloud Firestore, Cloud Messaging, and other backend services for web and mobile apps.",
}

RESEARCH_SYSTEM_PROMPT = """You are a senior business and technology analyst specializing in real-time communication platforms.

Your task is to research and gather factual, up-to-date information about companies in the real-time messaging and communication infrastructure space.

Guidelines:
- Focus on verifiable facts: product features, pricing, partnerships, funding, customer wins, technical capabilities.
- Prioritize information from 2025-2026 where available.
- Note the source URL for every key claim.
- Be specific with numbers: pricing tiers, message limits, latency figures, uptime SLAs.
- If information is uncertain or outdated, say so explicitly.
- Cover: product updates, pricing changes, new features, partnerships, market positioning, developer experience."""

SYNTHESIS_SYSTEM_PROMPT = """You are a senior business strategist producing a competitive analysis report for a real-time communication platform company.

You will receive research findings about PubNub and its competitors (Ably, Pusher, Firebase). Synthesize these findings into a structured, actionable report.

Guidelines:
- Be objective and data-driven. Support claims with evidence from the research.
- Include specific numbers where available (pricing, limits, performance metrics).
- Clearly distinguish between verified facts and analyst interpretation.
- Make strategic recommendations actionable and specific.
- Format the report in clean markdown with tables where appropriate.
- Include source URLs as footnotes or inline references."""


def get_research_queries():
    """Return the list of research queries to execute."""
    queries = []

    # Per-company queries
    for company, description in COMPANIES.items():
        queries.append({
            "id": f"company_{company.lower().replace(' ', '_')}",
            "label": f"{company} Research",
            "query": (
                f"What are the latest developments, product updates, features, "
                f"pricing changes, and market position of {company} in 2025-2026? "
                f"{company} is: {description} "
                f"Include any recent news, partnerships, funding rounds, "
                f"customer case studies, and technical capabilities."
            ),
        })

    # Cross-company pricing/feature comparison
    queries.append({
        "id": "pricing_comparison",
        "label": "Pricing & Feature Comparison",
        "query": (
            "Compare the pricing plans and key features of PubNub vs Ably vs Pusher vs Firebase "
            "for real-time messaging and communication in 2025-2026. "
            "Include: free tier limits, paid plan pricing, message throughput, "
            "connection limits, latency SLAs, supported protocols, and unique differentiators."
        ),
    })

    # Market trends
    queries.append({
        "id": "market_trends",
        "label": "Market Trends",
        "query": (
            "What are the key trends in the real-time communication and messaging platform market "
            "in 2025-2026? Include: market size estimates, growth drivers, emerging use cases "
            "(AI agents, IoT, live commerce, collaborative apps), technology shifts "
            "(WebTransport, WebCodecs, edge computing), and consolidation or M&A activity."
        ),
    })

    return queries
