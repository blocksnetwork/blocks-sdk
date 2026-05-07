import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import Ajv2020 from 'ajv/dist/2020.js';
import { describe, expect, it } from 'vitest';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// Navigate from sdks/node/tests/ up to the blocks-sdk root
const sdkRoot = path.resolve(__dirname, '..', '..', '..');

const SCHEMA_PATH = path.join(sdkRoot, 'schemas', 'agent-card.schema.json');

function discoverAgentCards(): string[] {
  const dirs = [
    path.join(sdkRoot, 'examples', 'node'),
    path.join(sdkRoot, 'examples', 'python'),
  ];

  const cards: string[] = [];
  for (const dir of dirs) {
    if (!fs.existsSync(dir)) continue;
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      const cardPath = path.join(dir, entry.name, 'agent-card.json');
      if (fs.existsSync(cardPath)) {
        cards.push(cardPath);
      }
    }
  }
  return cards;
}

function loadJson(filePath: string): Record<string, unknown> {
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

describe('example agent-card.json validation', () => {
  const schema = loadJson(SCHEMA_PATH);
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  const validate = ajv.compile(schema);
  const cards = discoverAgentCards();

  it('discovers at least one agent card', () => {
    expect(cards.length).toBeGreaterThan(0);
  });

  for (const cardPath of cards) {
    const label = path.relative(sdkRoot, cardPath);

    describe(label, () => {
      const card = loadJson(cardPath);

      it('validates against the input schema', () => {
        const valid = validate(card);
        if (!valid) {
          const errors = validate.errors
            ?.map((e) => `${e.instancePath} ${e.message}`)
            .join('; ');
          expect.fail(
            `Schema validation failed for ${label}: ${errors}`,
          );
        }
      });

      it('has identity.agentName and runtime.handler', () => {
        const identity = card.identity as
          | Record<string, unknown>
          | undefined;
        const runtime = card.runtime as
          | Record<string, unknown>
          | undefined;
        expect(identity, 'missing identity section').toBeDefined();
        expect(identity?.agentName, 'missing identity.agentName').toBeTruthy();
        expect(runtime, 'missing runtime section').toBeDefined();
        expect(runtime?.handler, 'missing runtime.handler').toBeTruthy();
      });

      it('references a handler file that exists', () => {
        const runtime = card.runtime as Record<string, unknown>;
        const handler = runtime.handler as string;
        const cardDir = path.dirname(cardPath);
        const handlerPath = path.resolve(cardDir, handler);
        expect(
          fs.existsSync(handlerPath),
          `handler file not found: ${handlerPath}`,
        ).toBe(true);
      });
    });
  }
});
