// Python mirror: tests/test_stream_wire_fixtures.py. Each SDK owns the fixture its StreamClient
// publishes and decodes every SDK's fixture, so a wire change on one side fails the other.
// Regenerate this SDK's fixture with UPDATE_STREAM_FIXTURES=1.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { StreamClient } from '../src/stream/stream-client.js';

type WireMessage = Record<string, unknown>;

interface StreamFixture {
  bytes: WireMessage[];
  events: WireMessage[];
}

const FIXTURES_DIR = new URL('../../../fixtures/streams/', import.meta.url);
const PRODUCERS = ['node', 'python'] as const;
const OWN_PRODUCER = 'node';
const FIXED_NOW_MS = 1_700_000_000_000;

const published: WireMessage[] = [];
const listeners: Array<{ message?: (event: { channel: string; message: unknown }) => void }> = [];

vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => ({
    setToken: vi.fn(),
    setFilterExpression: vi.fn(),
    addListener: vi.fn((listener) => listeners.push(listener)),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    destroy: vi.fn(),
    publish: vi.fn(async ({ message }: { message: WireMessage }) => {
      published.push(message);
      return { timetoken: '17000000000000000' };
    }),
    hereNow: vi.fn().mockResolvedValue({ channels: {} }),
  })),
}));

// Deterministic LCG; the Python mirror generates the same bytes from the same seed.
function deterministicBytes(size: number, seed: number): Uint8Array {
  const out = new Uint8Array(size);
  let s = seed >>> 0;
  for (let i = 0; i < size; i++) {
    s = ((Math.imul(s, 1103515245) + 12345) >>> 0) & 0x7fffffff;
    out[i] = s & 0xff;
  }
  return out;
}

// A 16384-byte write exceeds the 16384 max message size once encoded, so it splits into multipart.
const BYTES_WRITES: readonly Uint8Array[] = [
  new Uint8Array(0),
  new Uint8Array(1024),
  new Uint8Array(1024).fill(0xff),
  new Uint8Array(16384).fill(0x42),
  deterministicBytes(4096, 0xbeef),
];

const EVENT_WRITES: readonly unknown[] = [
  { type: 'primitive', n: 42, b: true, s: 'hello', nullValue: null },
  { type: 'nested', meta: { tags: ['a', 'b', 'c'], count: 7, deep: { x: { y: 'z' } } } },
  { type: 'special', text: 'emoji \u{1F680} RTL מבחן' },
  ...Array.from({ length: 10 }, (_, i) => ({ type: 'batch', i })),
];

function makeClient(format: 'bytes' | 'events', direction: 'inbound' | 'outbound'): StreamClient {
  return new StreamClient({
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
    token: 'token',
    agentName: 'fixture_agent',
    streamId: `fixture-${format}`,
    format,
    direction,
    maxMessageSize: 16384,
    bundleSizeBytes: 4096,
    maxLatencyMs: 60_000,
    gating: false,
  });
}

// Multipart parts publish concurrently, so order the capture for a stable fixture.
function wireOrder(a: WireMessage, b: WireMessage): number {
  const rank = (m: WireMessage) => [
    m.type === 'stream_end' ? 1 : 0,
    m.seq as number,
    (m.multipart as { part?: number } | undefined)?.part ?? 0,
  ];
  const [ra, rb] = [rank(a), rank(b)];
  return ra[0] - rb[0] || ra[1] - rb[1] || ra[2] - rb[2];
}

async function produce(format: 'bytes' | 'events', writes: readonly unknown[]): Promise<WireMessage[]> {
  published.length = 0;
  const client = makeClient(format, 'outbound');
  for (const write of writes) client.write(write);
  await client.end();
  return [...published].sort(wireOrder);
}

function feed(client: StreamClient, messages: WireMessage[]): void {
  const listener = listeners.at(-1);
  for (const message of messages) listener?.message?.({ channel: client.channel, message });
}

function concat(chunks: readonly Uint8Array[]): Uint8Array {
  const out = new Uint8Array(chunks.reduce((n, c) => n + c.byteLength, 0));
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function fixturePath(producer: string): string {
  return fileURLToPath(new URL(`${producer}-produced.json`, FIXTURES_DIR));
}

function readFixture(producer: string): StreamFixture {
  return JSON.parse(readFileSync(fixturePath(producer), 'utf-8')) as StreamFixture;
}

describe('stream wire fixtures', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(FIXED_NOW_MS);
    listeners.length = 0;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it(`${OWN_PRODUCER}-produced.json matches what this SDK publishes`, async () => {
    const actual: StreamFixture = {
      bytes: await produce('bytes', BYTES_WRITES),
      events: await produce('events', EVENT_WRITES),
    };
    if (process.env.UPDATE_STREAM_FIXTURES === '1') {
      writeFileSync(fixturePath(OWN_PRODUCER), `${JSON.stringify(actual, null, 2)}\n`);
    }
    expect(actual, 'stale fixture: rerun with UPDATE_STREAM_FIXTURES=1').toEqual(
      readFixture(OWN_PRODUCER),
    );
  });

  describe.each(PRODUCERS)('decodes %s-produced.json', (producer) => {
    it('bytes stream to the bytes written', async () => {
      const client = makeClient('bytes', 'inbound');
      feed(client, readFixture(producer).bytes);
      const chunks: Uint8Array[] = [];
      for await (const chunk of client.bytes()) chunks.push(chunk);
      expect(Buffer.from(concat(chunks)).equals(Buffer.from(concat(BYTES_WRITES)))).toBe(true);
    });

    it('events stream to the events written, in order', async () => {
      const client = makeClient('events', 'inbound');
      feed(client, readFixture(producer).events);
      const events: unknown[] = [];
      for await (const event of client.events()) events.push(event);
      expect(events).toEqual(EVENT_WRITES);
    });
  });
});
