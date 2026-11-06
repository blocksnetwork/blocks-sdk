/**
 * Session Echo agent -- starts an instance that serves sessions and tasks.
 *
 * `blocks run` starts task handlers only, so this script calls
 * `startAgentInstance()` itself to register the session handlers.
 *
 * Usage:
 *   npx tsx agent.ts
 *
 * Environment variables:
 *   BLOCKS_API_KEY  -- Blocks API key (required)
 *   BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
 */

import 'dotenv/config';
import { readFileSync } from 'node:fs';
import { startAgentInstance, type AgentCard } from '@blocks-network/sdk';
import handler, { onExchange, onSessionClose, onSessionOpen } from './handler.js';

const card = JSON.parse(readFileSync(new URL('./agent-card.json', import.meta.url), 'utf-8')) as AgentCard;

const instance = await startAgentInstance({
  agentName: card.identity.agentName,
  description: card.identity.description,
  card,
  handler,
  onSessionOpen,
  onExchange,
  onSessionClose,
  maxSessions: 4,
});

console.log(`[session-echo] instance ${instance.instanceId} running; press Ctrl+C to stop`);

const shutdown = () => {
  instance.stop();
  process.exit(0);
};
process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
