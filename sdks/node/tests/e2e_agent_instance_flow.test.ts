/**
 * E2E agent instance flow tests (stubbed, no network).
 *
 * Phase 3 update: mock createPubNubClient so per-task PubNub clients
 * use the same fake publish/subscribe stubs as the control client.
 */
import { describe, it, expect, vi } from 'vitest';

// Mock createPubNubClient so per-task PubNub clients use a shared fake
const sharedPublish = vi.fn();

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => ({
    publish: sharedPublish,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
    destroy: vi.fn(),
  })),
}));

import { startAgentInstance, type AgentInstanceOptions } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

// This is an in-memory end-to-end simulation (no PubNub network) using stubs.

/** Published message record for assertions. */
interface PublishedMessage {
  channel: string;
  message: Record<string, unknown>;
}

/** Minimal listener interface for PubNub stub. */
interface StubListener {
  message?: (event: { message: unknown; channel: string }) => void;
}

/** Minimal PubNub stub for e2e testing. */
interface StubPubNub {
  publish: ReturnType<typeof vi.fn>;
  addListener: (l: StubListener) => void;
  removeListener: ReturnType<typeof vi.fn>;
  subscribe: ReturnType<typeof vi.fn>;
  unsubscribe: ReturnType<typeof vi.fn>;
  published: PublishedMessage[];
}

const createFakePubNub = (): { pubnub: StubPubNub; listeners: StubListener[]; published: PublishedMessage[] } => {
  const published: PublishedMessage[] = [];
  const listeners: StubListener[] = [];
  const pubnub: StubPubNub = {
    publish: vi.fn().mockImplementation(async (args: { channel: string; message: Record<string, unknown> }) => {
      published.push({ channel: args.channel, message: args.message });
      // mimic PubNub by notifying listeners on publish
      listeners.forEach(
        (l) => l.message && l.message({ message: args.message, channel: args.channel }),
      );
      return { timetoken: Date.now().toString() };
    }),
    addListener: (l: StubListener) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    get published() {
      return published;
    },
  };
  return { pubnub, listeners, published };
};

describe('E2E agent instance flow (stubbed)', () => {
  it('runs send -> agent instance -> updates/index with handler', async () => {
    const { pubnub, published, listeners } = createFakePubNub();

    // Wire the shared publish mock to also push into the published array
    sharedPublish.mockImplementation(async (args: { channel: string; message: Record<string, unknown> }) => {
      published.push({ channel: args.channel, message: args.message });
      return { timetoken: Date.now().toString() };
    });
    const handler = vi
      .fn()
      .mockResolvedValue({ artifacts: [{ data: Buffer.from('hello world'), mimeType: 'text/plain' }] });
    // Cast to unknown since StubPubNub is a minimal test double
    await startAgentInstance({ pubnub: pubnub as unknown as AgentInstanceOptions['pubnub'], handler, agentName: 'acme_echo', card: makeTestCard() });

    // Simulate messageSend publishing StartTask (fire-and-forget)
    const msgListener = listeners[0];
    expect(msgListener).toBeDefined();
    msgListener.message?.({ message: { type: 'StartTask', taskId: 'e2e-task', requestParts: [] }, channel: 'test' });

    // Wait for fire-and-forget task to complete
    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    await vi.waitFor(() => {
      const messages = published.map((p) => p.message);
      const hasTerminal = messages.some((m) => m.type === 'terminal' && m.state === 'completed');
      expect(hasTerminal).toBe(true);
    });

    const messages = published.map((p) => p.message);
    const hasArtifact = messages.some((m) => m.type === 'artifact');
    expect(hasArtifact).toBe(true);
  });
});
