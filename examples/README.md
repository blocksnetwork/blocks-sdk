# Community & Demo Examples

These are **non-canonical** examples that demonstrate various agent
patterns but are not part of the maintained canonical teaching set.

They may use older SDK patterns, reference external APIs, or have
incomplete documentation. They are preserved for reference and
inspiration, not as production-ready templates.

For canonical, maintained examples, see:

- [Node canonical examples](../blocks-sdk/examples/node/README.md)
- [Python canonical examples](../blocks-sdk/examples/python/README.md)

## Node

| Example | Description |
|---------|-------------|
| [data-transformer](node/data-transformer/) | Transforms structured data between formats |
| [image-generator](node/image-generator/) | Generates images via external API (Gemini) |
| [powerpoint-creator](node/powerpoint-creator/) | Creates PowerPoint presentations via Python pptx |
| [report-generator](node/report-generator/) | Generates reports from templates |
| [sentiment-analyzer](node/sentiment-analyzer/) | Rule-based sentiment analysis |
| [trivia-generator](node/trivia-generator/) | Generates trivia questions via external API (OpenAI) |

## Python

| Example | Description |
|---------|-------------|
| [langchain-business-strategist](python/langchain-business-strategist/) | LangChain-based business strategy pipeline |

## Status

These examples were originally in `packages/node-examples/` and
`packages/python-examples/`. The canonical examples now live in `blocks-sdk/examples/`. They use current Blocks SDK
naming (`@blocks-network/sdk`, `blocks-network`) and each Node example
has a `main.ts` entrypoint (run via `npm start`). They are not actively
maintained or tested to the same standard as canonical examples. See any
canonical example for the recommended patterns.
