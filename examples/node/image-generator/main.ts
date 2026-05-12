import 'dotenv/config';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { startAgentInstance } from '@blocks-network/sdk';

const __dirname = dirname(fileURLToPath(import.meta.url));
const cardPath = resolve(__dirname, 'agent-card.json');
const card = JSON.parse(readFileSync(cardPath, 'utf-8'));

const runtime = card.runtime ?? {};
const handlerPath = resolve(__dirname, runtime.handler ?? './handler.ts');
const handlerExport = runtime.handlerExport ?? 'default';
const mod = await import(handlerPath);
const handler = mod[handlerExport];

console.log(`[agent] starting "${card.identity.displayName}" (${card.identity.agentName})`);

const instance = await startAgentInstance({
  handler,
  agentName: card.identity.agentName,
  description: card.identity.description,
  concurrency: runtime.concurrency ?? 1,
  expectedInstances: runtime.expectedInstances ?? 1,
  card,
});

console.log(`[agent] instance ${instance.instanceId} running`);
console.log('[agent] press Ctrl+C to stop');

const shutdown = () => {
  console.log('\n[agent] shutting down...');
  instance.stop();
  process.exit(0);
};
process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
