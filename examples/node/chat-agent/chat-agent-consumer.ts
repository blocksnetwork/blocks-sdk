/**
 * chat-agent consumer (Node).
 *
 * An interactive multi-turn chat REPL against the chat-agent. You type a
 * message, the agent replies, and the earlier context is still there on the
 * next message. Each turn is a separate `request` task; the consumer reads the
 * `conversationId` from the first turn's artifact and threads it into every
 * following turn so the agent can recall earlier context.
 *
 * This is the consumer-side counterpart to the multi-turn pattern: the wire
 * protocol has no conversation field, so the id is carried inside the request
 * part's JSON `text` and echoed back in the artifact.
 *
 * Usage:
 *   npx tsx chat-agent-consumer.ts                       # interactive REPL
 *   npx tsx chat-agent-consumer.ts "I'm Sam" "what's my name?"  # scripted turns, then exit
 *
 * In the REPL, type `exit` or `quit` (or press Ctrl-D) to end the conversation.
 *
 * Environment variables:
 *   BLOCKS_API_KEY      -- Blocks API key (required; run `blocks login --write-env`)
 *   BLOCKS_BACKEND_URL  -- backend base URL (optional; defaults to CDM config)
 *   BLOCKS_CDM_URL      -- CDM config URL (optional)
 */

import 'dotenv/config';
import * as readline from 'node:readline/promises';
import { stdin as input, stdout as output } from 'node:process';
import { TaskClient, fetchCdmConfig, getAgent } from '@blocks-network/sdk';

const AGENT_NAME = 'chat_agent_node';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error('BLOCKS_API_KEY not set. Run `blocks login --write-env` first.');
  process.exit(1);
}

const cdmUrl = process.env.BLOCKS_CDM_URL;
const baseUrl = process.env.BLOCKS_BACKEND_URL ?? (await fetchCdmConfig(cdmUrl)).api.baseUrl;

// Pass the API key so the registry lookup is authenticated: the
// GET /registry/agents route uses optionalAuth, so an anonymous lookup
// only sees *public* agents. A freshly published agent is private to your
// org and is invisible without the key.
const entry = await getAgent(AGENT_NAME, { baseUrl, apiKey });
if (!entry) {
  console.error(`Agent "${AGENT_NAME}" not found at ${baseUrl}. Run \`blocks publish\` in this folder first.`);
  process.exit(1);
}
const billingMode = entry.billingMode ?? 'free';

const client = await TaskClient.create({ billingMode, apiKey, cdmUrl, baseUrl });

async function sendTurn(text: string, conversationId: string | null): Promise<{
  reply: string;
  conversationId: string;
  turn: number;
}> {
  const message: Record<string, unknown> = { text };
  if (conversationId) message.conversationId = conversationId;

  const session = await client.sendMessage({
    agentName: AGENT_NAME,
    requestParts: [{ partId: 'message', text: JSON.stringify(message), contentType: 'application/json' }],
  });

  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => {
      session.close();
      reject(new Error('Timed out waiting for terminal after 30s'));
    }, 30_000);
    session.onTerminal(() => {
      clearTimeout(timeout);
      resolve();
    });
  });

  const refs = session.listArtifacts();
  if (refs.length === 0) {
    session.close();
    throw new Error('Agent returned no artifact.');
  }
  const downloaded = await session.downloadArtifact(refs[0]);
  const parsed = JSON.parse(new TextDecoder().decode(downloaded.data));
  session.close();

  return { reply: parsed.reply, conversationId: parsed.conversationId, turn: parsed.turn };
}

function isExit(text: string): boolean {
  const t = text.trim().toLowerCase();
  return t === 'exit' || t === 'quit';
}

let conversationId: string | null = null;
let turnsSent = 0;

async function say(text: string): Promise<void> {
  const result = await sendTurn(text, conversationId);
  conversationId = result.conversationId;
  turnsSent = result.turn;
  console.log(`[agent] ${result.reply}`);
  console.log(`        (conversationId=${result.conversationId}, turn=${result.turn})`);
}

async function runScripted(turns: string[]): Promise<void> {
  for (const text of turns) {
    console.log(`\n[you]   ${text}`);
    await say(text);
  }
}

async function runInteractive(): Promise<void> {
  console.log('Interactive chat with the agent. Type `exit` or `quit` (or Ctrl-D) to end.\n');
  const rl = readline.createInterface({ input, output });
  try {
    while (true) {
      let text: string;
      try {
        text = await rl.question('[you]   ');
      } catch {
        break; // Ctrl-D / stream closed
      }
      if (text.trim() === '') continue;
      if (isExit(text)) break;
      await say(text);
      console.log('');
    }
  } finally {
    rl.close();
  }
}

async function main() {
  const args = process.argv.slice(2);

  if (args.length > 0) {
    await runScripted(args);
  } else {
    await runInteractive();
  }

  console.log(
    turnsSent > 1
      ? '\nConversation complete. The agent recalled context from earlier turns.'
      : '\nConversation complete.',
  );
  client.destroy();
}

main().catch((err) => {
  console.error(err);
  client.destroy();
  process.exit(1);
});
