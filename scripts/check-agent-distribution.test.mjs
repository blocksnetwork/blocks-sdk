import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { checkAgentDistribution } from './check-agent-distribution.mjs';

const schema = '{"type":"object"}\n';
const license = 'Fixture license\n';

async function createDistributionFixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'agent-distribution-'));
  t.after(() => rm(root, { recursive: true, force: true }));

  await Promise.all([
    mkdir(path.join(root, 'schemas'), { recursive: true }),
    mkdir(path.join(root, 'skills', 'blocks-getstarted'), { recursive: true }),
    mkdir(path.join(root, 'skills', 'blocks-network', 'references'), { recursive: true }),
  ]);

  await Promise.all([
    writeFile(
      path.join(root, 'plugin.json'),
      JSON.stringify({
        $schema: 'https://agent-plugins.org/schemas/1.0.0/plugin.schema.json',
        name: 'blocks-network',
      }),
    ),
    writeFile(path.join(root, 'LICENSE'), license),
    writeFile(path.join(root, 'schemas', 'agent-card.schema.json'), schema),
    writeFile(path.join(root, 'skills', 'blocks-getstarted', 'LICENSE'), license),
    writeFile(
      path.join(root, 'skills', 'blocks-getstarted', 'SKILL.md'),
      skillMarkdown('blocks-getstarted'),
    ),
    writeFile(path.join(root, 'skills', 'blocks-network', 'LICENSE'), license),
    writeFile(
      path.join(root, 'skills', 'blocks-network', 'SKILL.md'),
      skillMarkdown('blocks-network'),
    ),
    writeFile(
      path.join(root, 'skills', 'blocks-network', 'references', 'agent-card.schema.json'),
      schema,
    ),
  ]);

  return root;
}

function skillMarkdown(name) {
  return `---\nname: ${name}\ndescription: Fixture skill\n---\n`;
}

test('accepts a valid agent distribution', async (t) => {
  const root = await createDistributionFixture(t);

  await checkAgentDistribution(root);
});

test('rejects plugin metadata that violates the Agent Plugins contract', async (t) => {
  const root = await createDistributionFixture(t);
  await writeFile(
    path.join(root, 'plugin.json'),
    JSON.stringify({
      $schema: 'https://agent-plugins.org/schemas/1.0.0/plugin.schema.json',
      name: 'blocks-network',
      version: 3,
      unexpected: true,
    }),
  );

  await assert.rejects(checkAgentDistribution(root), /plugin\.json contains an unsupported field/);
});

test('rejects a stale packaged schema', async (t) => {
  const root = await createDistributionFixture(t);
  await writeFile(
    path.join(root, 'skills', 'blocks-network', 'references', 'agent-card.schema.json'),
    '{"type":"string"}\n',
  );

  await assert.rejects(checkAgentDistribution(root), /agent-card\.schema\.json is stale/);
});

test('rejects a broken local Markdown reference', async (t) => {
  const root = await createDistributionFixture(t);
  await writeFile(
    path.join(root, 'skills', 'blocks-network', 'SKILL.md'),
    `${skillMarkdown('blocks-network')}[Missing](references/missing.md)\n`,
  );

  await assert.rejects(
    checkAgentDistribution(root),
    /has a missing local reference: references\/missing\.md/,
  );
});

test('rejects a relative dependency on a sibling skill', async (t) => {
  const root = await createDistributionFixture(t);
  await writeFile(
    path.join(root, 'skills', 'blocks-getstarted', 'SKILL.md'),
    `${skillMarkdown('blocks-getstarted')}Use \`../blocks-network/SKILL.md\`.\n`,
  );

  await assert.rejects(
    checkAgentDistribution(root),
    /blocks-getstarted\/SKILL\.md references sibling skill blocks-network/,
  );
});
