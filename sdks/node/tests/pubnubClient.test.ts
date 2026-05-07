import { describe, expect, it, vi, afterEach } from 'vitest';

const setup = async () => {
  vi.resetModules();

  const instances: Array<Record<string, unknown>> = [];
  const PubNub = vi.fn(function(this: unknown, opts: Record<string, unknown>) {
    instances.push(opts);
  });

  vi.doMock('pubnub', () => ({ default: PubNub }));

  const { createPubNubClient } = await import('../src/runtime/pubnub-client.js');
  return { createPubNubClient, instances };
};

afterEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
  vi.unmock('pubnub');
});

describe('createPubNubClient', () => {
  it('throws when PubNub keys are missing', async () => {
    const { createPubNubClient } = await setup();

    expect(() => createPubNubClient({
      publishKey: '',
      subscribeKey: '',
    })).toThrow('PUBNUB keys not configured');
  });

  it('creates client with explicit config', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      userId: 'explicit-user',
    });
    expect(instances[0]).toMatchObject({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      userId: 'explicit-user',
    });
  });

  it('does not accept secretKey', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]).not.toHaveProperty('secretKey');
  });

  it('falls back to default userId when none provided', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]?.userId).toBe('blocks-agent');
  });
});
