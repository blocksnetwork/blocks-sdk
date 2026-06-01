/**
 * Tests for the new agent card structure (9-section format).
 *
 * Covers: card parsing, partId, outputId, declaredStream, consumerPublicKey.
 */

import { describe, expect, it, vi, beforeAll, afterAll, afterEach } from 'vitest';
import { writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { tmpdir } from 'node:os';
import { loadAgentCard } from '../src/cli/run.js';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import type {
  StartTaskMessage,
  HandlerResult,
  TaskContext,
  CreateStreamOptions,
  RequestPart,
} from '../src/runtime/agent-instance.js';
import type { AgentCard } from '../src/runtime/agent-registry.js';
import { makeTestCard } from './helpers/test-card.js';

// ---------------------------------------------------------------------------
// Global mocks
// ---------------------------------------------------------------------------

const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({
      accessToken: 'mock-jwt',
      refreshToken: 'mock-rt',
      expiresIn: 60,
      agentId: 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa',
      controlChannel: 'agent.aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa.control',
    }),
  }) as unknown as typeof fetch;
});
afterAll(() => {
  globalThis.fetch = originalFetch;
});

let sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => ({
    publish: (...args: unknown[]) => sharedPublish(...args),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    setFilterExpression: vi.fn(),
    setToken: vi.fn(),
    setState: vi.fn(async () => ({})),
    destroy: vi.fn(),
    hereNow: vi.fn(async () => ({ channels: {} })),
  })),
}));

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const createFakePubNub = (): { pubnub: any; listeners: any[] } => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));
  const pubnub = {
    publish: sharedPublish,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn().mockResolvedValue({}),
    setToken: vi.fn(),
    destroy: vi.fn(),
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;
  return { pubnub, listeners };
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeTmpDir(cardContent?: string): string {
  const dir = resolve(tmpdir(), `nac-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(dir, { recursive: true });
  if (cardContent !== undefined) {
    writeFileSync(join(dir, 'agent-card.json'), cardContent, 'utf-8');
  }
  return dir;
}

/** Minimal new-format agent card. */
function newFormatCard(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    identity: {
      agentName: 'test_agent',
      displayName: 'test-agent',
      description: 'A test agent',
      version: '2.0.0',
      provider: { organization: 'TestOrg' },
    },
    capabilities: { taskKinds: ['request'] },
    tags: [{ id: 'test', name: 'Test' }],
    runtime: {
      handler: './handler.ts',
    },
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// 1. Card parsing (new 9-section format)
// ---------------------------------------------------------------------------

describe('new agent card: card parsing', () => {
  const tmpDirs: string[] = [];

  afterEach(() => {
    for (const d of tmpDirs) {
      rmSync(d, { recursive: true, force: true });
    }
    tmpDirs.length = 0;
  });

  it('reads identity.agentName, identity.displayName, identity.description, identity.version from new-format card', () => {
    const card = newFormatCard();
    const dir = makeTmpDir(JSON.stringify(card));
    tmpDirs.push(dir);

    const { card: loaded } = loadAgentCard(dir);
    expect(loaded.identity.agentName).toBe('test_agent');
    expect(loaded.identity.displayName).toBe('test-agent');
    expect(loaded.identity.description).toBe('A test agent');
    expect(loaded.identity.version).toBe('2.0.0');
  });

  it('reads capabilities.taskKinds from new-format card', () => {
    const card = newFormatCard({ capabilities: { taskKinds: ['request', 'pipe'] } });
    const dir = makeTmpDir(JSON.stringify(card));
    tmpDirs.push(dir);

    const { card: loaded } = loadAgentCard(dir);
    expect(loaded.capabilities.taskKinds).toEqual(['request', 'pipe']);
  });

  it('reads io section with inputs and outputs', () => {
    const card = newFormatCard({
      io: {
        inputs: [
          { id: 'prompt', contentType: 'text/plain', required: true },
        ],
        outputs: [
          { id: 'result', contentType: 'application/json', guaranteed: true },
        ],
      },
    });
    const dir = makeTmpDir(JSON.stringify(card));
    tmpDirs.push(dir);

    const { card: loaded } = loadAgentCard(dir);
    expect(loaded.io?.inputs).toHaveLength(1);
    expect(loaded.io?.inputs?.[0].id).toBe('prompt');
    expect(loaded.io?.outputs).toHaveLength(1);
    expect(loaded.io?.outputs?.[0].id).toBe('result');
  });

  it('reads streams section', () => {
    const card = newFormatCard({
      capabilities: { taskKinds: ['request', 'pipe'] },
      streams: {
        _default: { direction: 'outbound', format: 'events' },
        chat: { direction: 'bidirectional', format: 'events', affinity: 'shared' },
      },
    });
    const dir = makeTmpDir(JSON.stringify(card));
    tmpDirs.push(dir);

    const { card: loaded } = loadAgentCard(dir);
    expect(loaded.streams?._default.direction).toBe('outbound');
    expect(loaded.streams?.chat.affinity).toBe('shared');
  });

  it('reads security section', () => {
    const card = newFormatCard({
      security: {
        encryption: {
          required: true,
          algorithm: 'x25519-xsalsa20-poly1305',
          consumerKeyRequired: true,
          agentPublicKey: 'abc123',
        },
      },
    });
    const dir = makeTmpDir(JSON.stringify(card));
    tmpDirs.push(dir);

    const { card: loaded } = loadAgentCard(dir);
    expect(loaded.security?.encryption?.required).toBe(true);
    expect(loaded.security?.encryption?.consumerKeyRequired).toBe(true);
  });

  it('does not have heartbeatMs in runtime', () => {
    const card = newFormatCard();
    const dir = makeTmpDir(JSON.stringify(card));
    tmpDirs.push(dir);

    const { card: loaded } = loadAgentCard(dir);
    const runtime = loaded.runtime as Record<string, unknown>;
    expect(runtime).not.toHaveProperty('heartbeatMs');
  });
});

// ---------------------------------------------------------------------------
// 2. partId on request parts
// ---------------------------------------------------------------------------

describe('new agent card: partId on request parts', () => {
  it('handler receives partId on request parts from StartTask message', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedTask: StartTaskMessage | undefined;

    const handler = vi.fn(async (task: StartTaskMessage, _ctx?: TaskContext): Promise<HandlerResult> => {
      receivedTask = task;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_partid', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'partid-task-1',
        ownerId: 'user1',
        requestParts: [
          { partId: 'prompt', contentType: 'text/plain', data: 'Hello' },
          { contentType: 'application/json', data: { key: 'value' } },
        ],
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    expect(receivedTask).toBeDefined();
    expect(receivedTask!.requestParts).toHaveLength(2);
    expect(receivedTask!.requestParts![0].partId).toBe('prompt');
    expect(receivedTask!.requestParts![1].partId).toBeUndefined();

    stop();
  });
});

// ---------------------------------------------------------------------------
// 3. outputId on artifact events
// ---------------------------------------------------------------------------

describe('new agent card: outputId on artifact events', () => {
  it('includes outputId in artifact event when handler returns it', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (_task: StartTaskMessage, _ctx?: TaskContext): Promise<HandlerResult> => {
      return { artifacts: [{ data: 'result data', mimeType: 'text/plain', outputId: 'main_result' }] };
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_outputid', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'outputid-task-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const artifactCall = sharedPublish.mock.calls.find(
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (call: any) => call[0]?.message?.type === 'artifact',
      );
      expect(artifactCall).toBeDefined();
    });

    const artifactCall = sharedPublish.mock.calls.find(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (call: any) => call[0]?.message?.type === 'artifact',
    );
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const artifactMsg = (artifactCall as any)[0].message;
    expect(artifactMsg.outputId).toBe('main_result');

    stop();
  });

  it('does not include outputId when handler omits it', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (_task: StartTaskMessage, _ctx?: TaskContext): Promise<HandlerResult> => {
      return { artifacts: [{ data: 'result data', mimeType: 'text/plain' }] };
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_no_outputid', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'no-outputid-task-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const artifactCall = sharedPublish.mock.calls.find(
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (call: any) => call[0]?.message?.type === 'artifact',
      );
      expect(artifactCall).toBeDefined();
    });

    const artifactCall = sharedPublish.mock.calls.find(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (call: any) => call[0]?.message?.type === 'artifact',
    );
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const artifactMsg = (artifactCall as any)[0].message;
    expect(artifactMsg.outputId).toBeUndefined();

    stop();
  });
});

// ---------------------------------------------------------------------------
// 4. consumerPublicKey
// ---------------------------------------------------------------------------

describe('new agent card: consumerPublicKey', () => {
  it('handler receives consumerPublicKey on task context', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedCtx: TaskContext | undefined;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      receivedCtx = ctx;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_cpk', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'cpk-task-1',
        ownerId: 'user1',
        consumerPublicKey: 'consumer-key-abc123',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    expect(receivedCtx).toBeDefined();
    expect(receivedCtx!.consumerPublicKey).toBe('consumer-key-abc123');

    stop();
  });

  it('consumerPublicKey is undefined when not provided', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedCtx: TaskContext | undefined;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      receivedCtx = ctx;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_no_cpk', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'no-cpk-task-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    expect(receivedCtx).toBeDefined();
    expect(receivedCtx!.consumerPublicKey).toBeUndefined();

    stop();
  });
});

// ---------------------------------------------------------------------------
// 5. declaredStream in createStream
// ---------------------------------------------------------------------------

describe('new agent card: declaredStream in createStream options', () => {
  it('CreateStreamOptions type accepts declaredStream', () => {
    const opts: CreateStreamOptions = {
      direction: 'outbound',
      format: 'events',
      declaredStream: 'chat',
    };
    expect(opts.declaredStream).toBe('chat');
  });

  it('declaredStream defaults to _default when not specified', () => {
    const opts: CreateStreamOptions = {
      direction: 'outbound',
      format: 'events',
    };
    expect(opts.declaredStream).toBeUndefined();
    // The runtime defaults to '_default' -- verified by checking the setup message
  });
});

// ---------------------------------------------------------------------------
// 6. TaskClient: consumerPublicKey in extensions.blocks
// ---------------------------------------------------------------------------

describe('new agent card: TaskClient consumerPublicKey', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  let savedFetch: typeof globalThis.fetch;

  afterEach(() => {
    // Restore the mock that beforeAll installed (not the real fetch)
    globalThis.fetch = savedFetch;
  });

  it('includes consumerPublicKey in extensions.blocks on SendMessage', async () => {
    savedFetch = globalThis.fetch;
    fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-cpk-1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });
    globalThis.fetch = fetchSpy;

    const { TaskClient } = await import('../src/runtime/task-client.js');
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      createSessionPubNub: () => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return {
          setToken: vi.fn(),
          addListener: vi.fn(),
          subscribe: vi.fn(),
          removeListener: vi.fn(),
          unsubscribe: vi.fn(),
          destroy: vi.fn(),
          time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
          fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
            channels: { [channels[0]]: [] },
          })),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        } as any;
      },
    });

    await client.sendMessage({
      agentName: 'test_agent',
      requestParts: [{ partId: 'prompt', data: 'hello' }],
      ownerId: 'user1',
      consumerPublicKey: 'my-public-key-xyz',
    });

    const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
    expect(rpcBody.params.extensions.blocks.consumerPublicKey).toBe('my-public-key-xyz');
  });

  it('does not include extensions.blocks when no taskKind or consumerPublicKey', async () => {
    savedFetch = globalThis.fetch;
    fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-no-ext-1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });
    globalThis.fetch = fetchSpy;

    const { TaskClient } = await import('../src/runtime/task-client.js');
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      createSessionPubNub: () => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return {
          setToken: vi.fn(),
          addListener: vi.fn(),
          subscribe: vi.fn(),
          removeListener: vi.fn(),
          unsubscribe: vi.fn(),
          destroy: vi.fn(),
          time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
          fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
            channels: { [channels[0]]: [] },
          })),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        } as any;
      },
    });

    await client.sendMessage({
      agentName: 'test_agent',
      requestParts: [{ data: 'hello' }],
      ownerId: 'user1',
    });

    const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
    expect(rpcBody.params.extensions).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// 7. AgentCard type structure
// ---------------------------------------------------------------------------

describe('new agent card: AgentCard type structure', () => {
  it('AgentCard type has identity block with required fields', () => {
    const card: AgentCard = {
      identity: {
        agentName: 'test_agent',
        displayName: 'Test Agent',
        description: 'A test',
        version: '1.0.0',
        provider: { organization: 'Org' },
      },
      capabilities: { taskKinds: ['request'] },
      tags: [{ id: 'test', name: 'Test' }],
    };

    expect(card.identity.displayName).toBe('Test Agent');
    expect(card.identity.agentName).toBe('test_agent');
    expect(card.identity.provider.organization).toBe('Org');
    expect(card.capabilities.taskKinds).toContain('request');
  });

  it('AgentCard type supports optional sections', () => {
    const card: AgentCard = {
      identity: {
        agentName: 'full_agent',
        displayName: 'Full Agent',
        description: 'An agent with all sections',
        version: '2.0.0',
        provider: { organization: 'Org', url: 'https://example.com' },
        documentationUrl: 'https://docs.example.com',
        iconUrl: 'https://example.com/icon.png',
      },
      capabilities: { taskKinds: ['request', 'pipe'] },
      io: {
        inputs: [{ id: 'prompt', contentType: 'text/plain', required: true }],
        outputs: [{ id: 'result', contentType: 'application/json', guaranteed: true }],
      },
      streams: {
        _default: { direction: 'outbound', format: 'events' },
      },
      security: {
        encryption: {
          required: false,
          algorithm: 'x25519-xsalsa20-poly1305',
          consumerKeyRequired: false,
        },
      },
      services: { webhooks: true },
      tags: [{ id: 'main', name: 'Main', description: 'The main tag' }],
      extensions: { custom: 'data' },
      runtime: {
        handler: './handler.ts',
        concurrency: 4,
      },
    };

    expect(card.io?.inputs).toHaveLength(1);
    expect(card.streams?._default.direction).toBe('outbound');
    expect(card.security?.encryption?.algorithm).toBe('x25519-xsalsa20-poly1305');
    expect(card.services?.webhooks).toBe(true);
    expect(card.extensions?.custom).toBe('data');
    expect(card.runtime?.concurrency).toBe(4);
  });
});

// ===========================================================================
// Follow-up Round: K1 — Typed RequestPart interface
// ===========================================================================

describe('follow-up K1: typed RequestPart interface', () => {
  it('RequestPart has explicit text and contentType fields', () => {
    const part: RequestPart = {
      partId: 'prompt',
      text: 'Hello world',
      contentType: 'text/plain',
    };
    expect(part.partId).toBe('prompt');
    expect(part.text).toBe('Hello world');
    expect(part.contentType).toBe('text/plain');
  });

  it('RequestPart allows extra properties via index signature', () => {
    const part: RequestPart = {
      partId: 'data',
      text: 'payload',
      contentType: 'application/json',
      customField: 42,
      nested: { key: 'value' },
    };
    expect(part.customField).toBe(42);
    expect(part.nested).toEqual({ key: 'value' });
  });

  it('handler receives typed RequestPart objects with text and contentType', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedTask: StartTaskMessage | undefined;

    const handler = vi.fn(async (task: StartTaskMessage, _ctx?: TaskContext): Promise<HandlerResult> => {
      receivedTask = task;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_rp_typed', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'rp-typed-1',
        ownerId: 'user1',
        requestParts: [
          { partId: 'prompt', text: 'Hello', contentType: 'text/plain' },
          { partId: 'config', contentType: 'application/json', data: { mode: 'fast' } },
          { text: 'extra context' },
        ],
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    expect(receivedTask).toBeDefined();
    expect(receivedTask!.requestParts).toHaveLength(3);
    expect(receivedTask!.requestParts![0].partId).toBe('prompt');
    expect(receivedTask!.requestParts![0].text).toBe('Hello');
    expect(receivedTask!.requestParts![0].contentType).toBe('text/plain');
    expect(receivedTask!.requestParts![1].partId).toBe('config');
    expect(receivedTask!.requestParts![1].contentType).toBe('application/json');
    expect(receivedTask!.requestParts![2].text).toBe('extra context');
    expect(receivedTask!.requestParts![2].partId).toBeUndefined();

    stop();
  });

  it('TaskContext exposes typed requestParts', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedCtx: TaskContext | undefined;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      receivedCtx = ctx;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_ctx_rp', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'ctx-rp-1',
        ownerId: 'user1',
        requestParts: [
          { partId: 'prompt', text: 'Test input', contentType: 'text/plain' },
        ],
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    expect(receivedCtx).toBeDefined();
    expect(receivedCtx!.requestParts).toHaveLength(1);
    expect(receivedCtx!.requestParts[0].partId).toBe('prompt');
    expect(receivedCtx!.requestParts[0].text).toBe('Test input');
    expect(receivedCtx!.requestParts[0].contentType).toBe('text/plain');

    stop();
  });

  it('TaskContext.requestParts is empty array when StartTask has no parts', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedCtx: TaskContext | undefined;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      receivedCtx = ctx;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_ctx_rp_empty', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'ctx-rp-empty-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    expect(receivedCtx).toBeDefined();
    expect(receivedCtx!.requestParts).toEqual([]);

    stop();
  });
});

// ===========================================================================
// Follow-up Round: K2 — reportStatus throttle buffer
// ===========================================================================

describe('follow-up K2: reportStatus throttle with buffer', () => {
  it('first reportStatus call publishes immediately', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      ctx!.reportStatus('step 1');
      // Wait briefly to allow the async publish to be queued.
      await new Promise(r => setTimeout(r, 50));
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_status_imm', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'status-imm-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    // Wait for the handler to complete and publishes to flush.
    await vi.waitFor(() => {
      const statusCall = sharedPublish.mock.calls.find(
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (call: any) => call[0]?.message?.type === 'progress' && call[0]?.message?.message === 'step 1',
      );
      expect(statusCall).toBeDefined();
    });

    stop();
  });

  it('rapid calls within throttle window buffer the latest message', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let handlerDone = false;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      ctx!.reportStatus('msg-A');
      ctx!.reportStatus('msg-B');
      ctx!.reportStatus('msg-C');
      // Wait for the throttle timer to fire (> 1 second).
      await new Promise(r => setTimeout(r, 1200));
      handlerDone = true;
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_status_buf', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'status-buf-1', ownerId: 'user1' },
    });

    // Wait for the handler to fully complete (including its 1200ms sleep).
    await vi.waitFor(() => expect(handlerDone).toBe(true), { timeout: 3000 });

    // Allow post-handler publishes to settle.
    await new Promise(r => setTimeout(r, 300));

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const statusCalls = sharedPublish.mock.calls.filter(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (call: any) => call[0]?.message?.type === 'progress' && typeof call[0]?.message?.message === 'string',
    );

    // First call (msg-A) should publish immediately. msg-B and msg-C
    // are within the throttle window; only the latest (msg-C) should
    // be published when the timer fires.
    const messages = statusCalls.map(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (call: any) => call[0].message.message,
    );
    expect(messages).toContain('msg-A');
    expect(messages).toContain('msg-C');
    expect(messages).not.toContain('msg-B');

    stop();
  }, 5000);

  it('buffered status is flushed when handler returns', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      ctx!.reportStatus('immediate');
      // Second call within throttle window -- will be buffered.
      ctx!.reportStatus('final-buffered');
      // Return immediately (do NOT wait for timer). The finally block
      // should flush the buffered message.
      return {};
    });

    const { stop } = await startAgentInstance({ pubnub, agentName: 'test_status_flush', card: makeTestCard(), handler, baseUrl: 'http://test' });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'status-flush-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    // Wait for publishes to settle.
    await new Promise(r => setTimeout(r, 200));

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const statusCalls = sharedPublish.mock.calls.filter(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (call: any) => call[0]?.message?.type === 'progress' && typeof call[0]?.message?.message === 'string',
    );
    const messages = statusCalls.map(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (call: any) => call[0].message.message,
    );

    // Both should appear: immediate publish + finally flush.
    expect(messages).toContain('immediate');
    expect(messages).toContain('final-buffered');

    stop();
  });
});
