// Environment variables are listed in fixtures/sessions/README.md, "Running".
import { performance } from 'node:perf_hooks';
import { removeAgent } from '../../../src/runtime/agent-registry.js';
import { getBaseUrl, hasBackendEnv, publishAgent } from '../../helpers/live-test-config.js';
import { makeTestCard } from '../../helpers/test-card.js';
import type {
  ArtifactRef,
  BlocksClient,
  ExchangeContext,
  HandlerResult,
  Session,
  SessionSdk,
} from './session-sdk.js';

export const liveGuard =
  process.env.PUBNUB_LIVE_TEST !== '1' || !hasBackendEnv() || !process.env.BLOCKS_API_KEY;
export const sessionsFlagOn = process.env.SESSIONS_FLAG_ON === '1';

export const TURN_TIMEOUT_MS = 30_000;
const ROUTE_READY_TIMEOUT_MS = 30_000;
const ROUTE_RETRY_MS = 500;
// Presence activeTasks lags the terminal publish, so concurrency 1 queues back-to-back tasks.
const PROVIDER_CONCURRENCY = 4;
const PAID_PRICE_PER_TASK = '0.01';

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function apiKey(): string {
  const key = process.env.BLOCKS_API_KEY;
  if (!key) throw new Error('BLOCKS_API_KEY is required for session live tests');
  return key;
}

export function createConsumer(
  sdk: SessionSdk,
  billingMode: 'free' | 'paid' = 'free',
): Promise<BlocksClient> {
  return sdk.BlocksClient.create({ billingMode, apiKey: apiKey(), baseUrl: getBaseUrl() });
}

export function artifactText(ref: ArtifactRef): string {
  if (ref.kind !== 'inline' || ref.data === undefined) {
    throw new Error(`expected an inline artifact, got ${JSON.stringify(ref)}`);
  }
  return Buffer.from(ref.data, 'base64').toString('utf8');
}

export interface EchoPayload {
  exchangeId: string;
  text: string;
  sdk: 'node' | 'python';
}

export function echoResult(exchange: ExchangeContext, sdk: EchoPayload['sdk']): HandlerResult {
  const text = exchange.requestParts.map((p) => p.text ?? '').join('');
  const payload: EchoPayload = { exchangeId: exchange.exchangeId, text, sdk };
  return { artifacts: [{ data: JSON.stringify(payload), mimeType: 'application/json' }] };
}

export const decodeEcho = (ref: ArtifactRef): EchoPayload =>
  JSON.parse(artifactText(ref)) as EchoPayload;

export const echoReply = (text: string) => `echo:${text}`;
export const taskReply = (text: string) => `task:${text}`;

export interface SeenExchange {
  sessionId: string;
  exchangeId: string;
  text: string;
  receivedAt: number;
}

export interface TestProvider {
  agentName: string;
  sessionsOpened: string[];
  sessionsClosed: Array<{ sessionId: string; reason: string }>;
  exchanges: SeenExchange[];
  tasks: Array<{ taskId: string; text: string }>;
  /** Stops the instance and keeps the agent published. */
  halt(): void;
  /** Stops the instance and deletes the agent. */
  stop(): Promise<void>;
}

export interface TestProviderOptions {
  agentName: string;
  billingMode?: 'free' | 'paid';
  // Also the route's maxSessions, which defaults to the instance's concurrency.
  concurrency?: number;
  beforeReply?: (text: string) => Promise<void>;
  onTask?: (text: string) => void;
}

async function publishTestAgent(agentName: string, billingMode: 'free' | 'paid' = 'free') {
  const card = makeTestCard({ agentName });
  await publishAgent(agentName, card, {
    billingMode,
    listing: 'private',
    ...(billingMode === 'paid'
      ? { pricePerTask: PAID_PRICE_PER_TASK, tcAcceptedAt: new Date().toISOString() }
      : {}),
  });
  return card;
}

export async function publishSessionAgent(prefix: string, created: string[]): Promise<string> {
  const agentName = uniqueAgentName(prefix);
  await publishTestAgent(agentName);
  created.push(agentName);
  return agentName;
}

export function deleteAgent(name: string): Promise<unknown> {
  return removeAgent(name, { baseUrl: getBaseUrl(), reason: 'session live test cleanup' });
}

export async function removeAgents(names: string[]): Promise<void> {
  for (const name of names) {
    await deleteAgent(name).catch((err: unknown) =>
      console.warn(`[cleanup] removeAgent(${name}) failed:`, err),
    );
  }
}

export async function startTestProvider(
  sdk: SessionSdk,
  opts: TestProviderOptions,
): Promise<TestProvider> {
  const { agentName, billingMode = 'free' } = opts;
  const card = await publishTestAgent(agentName, billingMode);

  const provider: TestProvider = {
    agentName,
    sessionsOpened: [],
    sessionsClosed: [],
    exchanges: [],
    tasks: [],
    halt: () => {},
    stop: async () => {},
  };
  const firstText = (parts: Array<{ text?: string }> | undefined) => parts?.[0]?.text ?? '';
  const textArtifact = (data: string) => ({ artifacts: [{ data, mimeType: 'text/plain' }] });

  const instance = await sdk.startAgentInstance({
    agentName,
    card,
    baseUrl: getBaseUrl(),
    concurrency: opts.concurrency ?? PROVIDER_CONCURRENCY,
    handler: async (task) => {
      const text = firstText(task.requestParts);
      provider.tasks.push({ taskId: task.taskId, text });
      opts.onTask?.(text);
      return textArtifact(taskReply(text));
    },
    onSessionOpen: (ctx) => {
      provider.sessionsOpened.push(ctx.sessionId);
    },
    onExchange: async (ctx, exchange) => {
      const text = firstText(exchange.requestParts);
      provider.exchanges.push({
        sessionId: ctx.sessionId,
        exchangeId: exchange.exchangeId,
        text,
        receivedAt: performance.now(),
      });
      await opts.beforeReply?.(text);
      return textArtifact(echoReply(text));
    },
    onSessionClose: (ctx, reason) => {
      provider.sessionsClosed.push({ sessionId: ctx.sessionId, reason });
    },
  });

  provider.halt = () => instance.stop();
  provider.stop = async () => {
    instance.stop();
    await removeAgents([agentName]);
  };
  return provider;
}

export const errorCode = (err: unknown): string | undefined =>
  (err as { code?: string } | undefined)?.code;

// The route opens after the instance starts, so an open can briefly meet SESSION_NO_ROUTE.
export async function openSessionWhenRouted(
  client: BlocksClient,
  agentName: string,
): Promise<Session> {
  const deadline = performance.now() + ROUTE_READY_TIMEOUT_MS;
  for (;;) {
    try {
      return await client.openSession(agentName);
    } catch (err) {
      if (errorCode(err) !== 'SESSION_NO_ROUTE' || performance.now() > deadline) throw err;
    }
    await sleep(ROUTE_RETRY_MS);
  }
}

// Scenarios that count backend calls start recording after this probe.
export async function waitForRoute(client: BlocksClient, agentName: string): Promise<void> {
  const probe = await openSessionWhenRouted(client, agentName);
  await probe.close();
}

export async function withRoutedAgent<T>(
  sdk: SessionSdk,
  opts: TestProviderOptions,
  run: (env: { provider: TestProvider; client: BlocksClient }) => Promise<T>,
): Promise<T> {
  const provider = await startTestProvider(sdk, opts);
  let client: BlocksClient | undefined;
  try {
    client = await createConsumer(sdk);
    await waitForRoute(client, provider.agentName);
    return await run({ provider, client });
  } finally {
    client?.destroy();
    await provider.stop();
  }
}

export interface BackendCall {
  method: string;
  path: string;
  status: number;
  authorization: string | null;
  body: unknown;
  /** performance.now() when the response arrived. */
  at: number;
}

const SESSION_PATH = /^\/api\/v1\/(sessions|provider\/(routes|checkpoints|sessions))(\/|$)/;

export const isSessionEndpoint = (call: BackendCall) => SESSION_PATH.test(call.path);

// Every SDK HTTP client uses global fetch, so wrapping it sees all backend calls.
export function recordBackendCalls(): { calls: BackendCall[]; restore: () => void } {
  const origin = new URL(getBaseUrl()).origin;
  const realFetch = globalThis.fetch;
  const calls: BackendCall[] = [];

  globalThis.fetch = async (input, init) => {
    const request = new Request(input, init);
    const response = await realFetch(input, init);
    const url = new URL(request.url);
    if (url.origin === origin) {
      const text = await response
        .clone()
        .text()
        .catch(() => '');
      let body: unknown = text;
      try {
        body = text ? JSON.parse(text) : undefined;
      } catch {
        // non-JSON body stays as text
      }
      calls.push({
        method: request.method,
        path: url.pathname,
        status: response.status,
        authorization: request.headers.get('authorization'),
        body,
        at: performance.now(),
      });
    }
    return response;
  };

  return {
    calls,
    restore: () => {
      globalThis.fetch = realFetch;
    },
  };
}

export const uniqueAgentName = (prefix: string) =>
  `${prefix}_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;

const ROUTE_ATTEMPT_TIMEOUT_MS = 30_000;

// Resolves with the provider's route request once it has been answered. No vitest here: the
// forked provider child imports this module outside the test runner.
export async function routeRequest(calls: BackendCall[]): Promise<BackendCall> {
  const find = () =>
    calls.find((c) => c.method === 'POST' && c.path === '/api/v1/provider/routes');
  const deadline = performance.now() + ROUTE_ATTEMPT_TIMEOUT_MS;
  for (;;) {
    const call = find();
    if (call) return call;
    if (performance.now() > deadline) throw new Error('no route request was answered in time');
    await sleep(ROUTE_RETRY_MS);
  }
}
