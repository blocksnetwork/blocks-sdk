# Blocks Network Agent Skills

This directory is the `skills/` component of the portable Agent Plugin declared
by [`../plugin.json`](../plugin.json). Each installable skill is an immediate
child directory containing a `SKILL.md` whose frontmatter `name` matches the
directory name.

```text
skills/
├── blocks-getstarted/
│   ├── LICENSE
│   └── SKILL.md
├── blocks-network/
│   ├── LICENSE
│   ├── SKILL.md
│   └── references/
│       ├── agent-card.schema.json
│       └── …
├── evals/
└── tile.json
```

- `blocks-getstarted` owns the linear first-agent workflow.
- `blocks-network` owns the non-linear reference workflow and the detailed
  reference bundle.
- Each skill must remain independently installable. Do not link to files in a
  sibling skill with `../<skill>/...`. Cross-skill handoffs name the target
  skill, while shared detailed references continue to use the stable
  `https://config.blocks.ai/` URLs until an independent bundling mechanism is
  designed.
- `blocks-network/references/agent-card.schema.json` is the distributable copy of
  the canonical `../schemas/agent-card.schema.json`. Keep it byte-identical so
  installations made by `npx skills` do not depend on files outside `skills/`.
- Each skill includes a distributable copy of the repository `LICENSE` because
  `npx skills` installs skill directories independently.
- `evals/` contains development-time evaluation fixtures and is not discovered
  as a skill because it has no `SKILL.md`.
- `tile.json` indexes the same canonical skill files for the existing tile
  distribution.

Do not add flat compatibility copies such as `skills/SKILL.md` or
`skills/GETSTARTED.md`. Distribution pipelines that preserve legacy hosted URLs
should map those URLs to the canonical files above rather than create a second
hand-maintained source. Do not replace the hosted URLs inside a skill with
cross-skill relative paths until independently installable reference bundles
have been designed.

Validate the plugin/skill boundaries and both skills from the `blocks-sdk`
repository root:

```bash
npm run check:agent-distribution
npx --yes skills-ref@0.1.5 validate skills/blocks-network
npx --yes skills-ref@0.1.5 validate skills/blocks-getstarted
npx --yes skills@latest add . --list
```
