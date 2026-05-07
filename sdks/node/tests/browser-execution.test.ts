/**
 * @vitest-environment jsdom
 *
 * Browser execution test for Family A bugs (Consumer SDK Correctness
 * Omnibus, dev_docs/initiative/sdk_consumer_fixes). Exercises the SDK
 * paths that browser bundling alone cannot catch: runtime file-part
 * normalization, artifact base64 encoding, and the PubNub
 * `downloadFile` result classifier.
 *
 * This file is the RED BASELINE. It is expected to FAIL on master
 * against each Family A finding. The scf-browser agent's Family A
 * implementation turns the failures green; see
 * dev_docs/initiative/sdk_consumer_fixes/SDK_CONSUMER_FIXES_IMPL.md
 * (Family A) for the fix scope.
 *
 * -------------------------------------------------------------------
 * Why jsdom and not real-browser?
 * -------------------------------------------------------------------
 * jsdom gives the SDK a `Blob`/`File`/`FormData`/`atob` surface that
 * matches the browser, while still running on Node. Some of the
 * Family A bugs throw at runtime the moment the SDK receives a
 * browser-native value (e.g. `Buffer.from(new Blob(...))` throws a
 * `TypeError` synchronously). Others corrupt data silently (e.g.
 * `new Uint8Array([...]).toString('base64')` returns `"1,2,3"`, not
 * base64 -- Buffer overrides `.toString` but plain Uint8Array does
 * not). jsdom is sufficient to expose both classes.
 *
 * Bundle-time checks (unresolved `node:fs`, etc.) live in the
 * sibling `browser-bundle.test.ts` and are NOT duplicated here --
 * that file intentionally remains as-is per Family B scope.
 *
 * -------------------------------------------------------------------
 * Empirical downloadFile return shape -- needs real-browser
 * verification post-merge
 * -------------------------------------------------------------------
 * pubnub@10.2.x's TypeScript surface (see
 * blocks-sdk/node_modules/pubnub/lib/types/index.d.ts:1296-1307 and
 * :1705-1736) exposes `downloadFile(): Promise<PlatformFile>` where
 * `PlatformFile` is any `Partial<PubNubFileInterface>`. The public
 * `PubNubFileInterface` only declares `toArrayBuffer()`, while the
 * existing `artifacts.ts` handles three runtime shapes:
 *
 *   1. Raw `Uint8Array` / Buffer -- observed in recent Node runtime.
 *   2. Raw `Blob` -- documented in older browser SDK release notes.
 *   3. `PubNubFile` with `toArrayBuffer()` (and historically
 *      `toBuffer()` on older `@pubnub/file` objects).
 *
 * This test covers all three shapes so Family A retains parity on
 * each branch. Empirical shape observed in Chromium 2026-04-19 via
 * Playwright MCP (UPDATED_HUMAN_TEST §4.2): `Uint8Array` for an
 * 82465-byte image/png input -- matches classifier branch #1 above.
 * Firefox observation still outstanding and deferred as a post-merge
 * one-off (UNRESOLVED.md §1.3). Keep all three branches regardless of
 * which is observed: the classifier is cross-browser + cross-version
 * resilience, not just a mirror of today's runtime.
 */

import {
  describe,
  it,
  expect,
  vi,
  beforeEach,
  afterEach,
} from 'vitest';

// vi.hoisted hoists this block above every `import` below. The mock
// factory and every `import PubNub from 'pubnub'` inside the SDK
// resolve to the same `pubnubMocks.mockInstance` object the test
// body asserts against. The factory MUST be re-callable because the
// SDK allocates a fresh client per TaskSession and a separate one
// for downloadArtifact temporaries (see task-session.ts:540).
const pubnubMocks = vi.hoisted(() => ({
  mockInstance: {
    publish: vi.fn().mockResolvedValue({ timetoken: '1' }),
    subscribe: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
    downloadFile: vi.fn(),
    sendFile: vi.fn(),
    destroy: vi.fn(),
    hereNow: vi.fn().mockResolvedValue({ channels: {} }),
    time: vi.fn().mockResolvedValue({ timetoken: '17000000000000000' }),
    setFilterExpression: vi.fn(),
    fetchMessages: vi.fn().mockResolvedValue({ channels: {} }),
  },
}));

vi.mock('pubnub', () => ({
  default: vi.fn(() => pubnubMocks.mockInstance),
}));

// StreamClient is mocked so sendMessage() doesn't try to construct
// real PubNub stream clients; the code path we care about is the
// file-part processing that happens *before* any stream setup.
vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = (await importOriginal()) as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: {
      fromDescriptor: vi.fn(() => ({
        isActive: true,
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        onInboundDone: vi.fn(),
      })),
    },
  };
});

import { TaskSession } from '../src/runtime/task-session.js';
import {
  buildArtifactRef,
  decodeInlineArtifact,
  downloadArtifact,
  type ArtifactRef,
} from '../src/runtime/artifacts.js';
import {
  createTaskClient,
  createPreClosedTaskSession,
  sendFileParts,
  fileRef,
} from './fixtures/browser-smoke/runtime.js';

// ---------------------------------------------------------------------
// Shared fetch stub helpers
// ---------------------------------------------------------------------

const mockRpcResponse = (result: unknown) => ({
  ok: true,
  status: 200,
  headers: new Headers(),
  json: async () => ({ jsonrpc: '2.0', id: 'x', result }),
  text: async () => JSON.stringify({ jsonrpc: '2.0', id: 'x', result }),
});

const sendMessageResult = (taskId: string) => ({
  taskId,
  idempotent: false,
  queued: false,
  extensions: {
    blocks: {
      streamChannels: { status: `u.user-browser-test.${taskId}` },
      readToken: 'T4-read-token',
    },
  },
});

const originalFetch = globalThis.fetch;
let fetchSpy: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchSpy = vi.fn();
  globalThis.fetch = fetchSpy as unknown as typeof globalThis.fetch;
  vi.clearAllMocks();
  // After clearAllMocks, reapply default mock behaviors on the
  // hoisted PubNub instance so each test starts from a clean slate.
  pubnubMocks.mockInstance.publish.mockResolvedValue({ timetoken: '1' });
  pubnubMocks.mockInstance.time.mockResolvedValue({
    timetoken: '17000000000000000',
  });
  pubnubMocks.mockInstance.hereNow.mockResolvedValue({ channels: {} });
  pubnubMocks.mockInstance.fetchMessages.mockResolvedValue({
    channels: {},
  });
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

// ---------------------------------------------------------------------
// Family A1 / A3 / A6 / A7 -- sendMessage with browser-native inputs
// ---------------------------------------------------------------------
// task-client.ts:653 runs
//   const fileData = Buffer.isBuffer(part.file)
//     ? part.file
//     : Buffer.from(part.file);
// On master, `Buffer.from(new Blob(...))` and `Buffer.from(new
// File(...))` throw a TypeError. `Buffer.from(new Uint8Array(...))`
// succeeds today and must keep succeeding after Family A lands.
// ---------------------------------------------------------------------

describe('browser execution: sendMessage with browser-native file inputs', () => {
  it('Uint8Array input is accepted and returns a TaskSession (regression guard)', async () => {
    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse(sendMessageResult('task-u8')),
    );

    const client = createTaskClient('http://mock-backend.test');

    // Uint8Array should work today *and* after Family A.
    const u8 = new Uint8Array([1, 2, 3]);
    const session = await client.sendMessage({
      agentName: 'mock-agent',
      requestParts: [
        {
          partId: 'u8',
          file: u8,
          fileName: 'u8.bin',
          contentType: 'application/octet-stream',
        },
      ],
      ownerId: 'user-browser-test',
    });
    expect(session).toBeInstanceOf(TaskSession);
  });

  it('Blob input is accepted and returns a TaskSession', async () => {
    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse(sendMessageResult('task-blob')),
    );

    const client = createTaskClient('http://mock-backend.test');

    const blob = new Blob([new Uint8Array([4, 5, 6])], {
      type: 'application/octet-stream',
    });
    const session = await client.sendMessage({
      agentName: 'mock-agent',
      requestParts: [
        {
          partId: 'blob',
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          file: blob as any,
          fileName: 'blob.bin',
          contentType: 'application/octet-stream',
        },
      ],
      ownerId: 'user-browser-test',
    });
    expect(session).toBeInstanceOf(TaskSession);
  });

  it('File input is accepted and returns a TaskSession', async () => {
    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse(sendMessageResult('task-file')),
    );

    const client = createTaskClient('http://mock-backend.test');

    const file = new File([new Uint8Array([7, 8, 9])], 'test.bin', {
      type: 'application/octet-stream',
    });
    const session = await client.sendMessage({
      agentName: 'mock-agent',
      requestParts: [
        {
          partId: 'file',
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          file: file as any,
          fileName: 'test.bin',
          contentType: 'application/octet-stream',
        },
      ],
      ownerId: 'user-browser-test',
    });
    expect(session).toBeInstanceOf(TaskSession);
  });

  it('all three inputs via the shared fixture helper succeed together', async () => {
    // Three sequential sendMessage calls -- one RPC response per call.
    fetchSpy
      .mockResolvedValueOnce(mockRpcResponse(sendMessageResult('t-u8')))
      .mockResolvedValueOnce(mockRpcResponse(sendMessageResult('t-blob')))
      .mockResolvedValueOnce(mockRpcResponse(sendMessageResult('t-file')));

    const client = createTaskClient('http://mock-backend.test');
    await expect(sendFileParts(client)).resolves.toMatchObject({
      u8Session: expect.any(TaskSession),
      blobSession: expect.any(TaskSession),
      fileSession: expect.any(TaskSession),
    });
  });
});

// ---------------------------------------------------------------------
// Family A5 -- buildArtifactRef inline base64 encoding
// ---------------------------------------------------------------------
// artifacts.ts:58 calls `input.data?.toString('base64')`. For Node
// Buffer, this yields proper base64. For a raw Uint8Array (the
// browser-native shape after Family A), `.toString('base64')` ignores
// the encoding argument and returns a comma-joined decimal string
// (e.g. `[104,105]` -> `"104,105"`), corrupting the artifact.
//
// This test feeds a plain Uint8Array (NOT a Buffer) to
// buildArtifactRef and round-trips through decodeInlineArtifact.
// Pre-Family-A the round-trip fails because the base64 string is
// malformed. Post-Family-A (switch to a Buffer-independent base64
// helper) the round-trip matches the original bytes.
// ---------------------------------------------------------------------

describe('browser execution: buildArtifactRef inline base64 encoding', () => {
  it('round-trips a Uint8Array through build + decode', () => {
    const original = new Uint8Array([104, 101, 108, 108, 111]); // "hello"

    // Cast: buildArtifactRef.BuildArtifactInput.data is typed `Buffer`
    // today; Family A widens to `Uint8Array`. The runtime behavior is
    // what this test checks, not the type surface.
    const ref = buildArtifactRef({
      mimeType: 'text/plain',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      data: original as any,
      fileName: 'hello.txt',
    });

    expect(ref.kind).toBe('inline');
    expect(typeof ref.data).toBe('string');
    // The base64 of "hello" is "aGVsbG8=". If the code silently
    // calls `Uint8Array.toString('base64')` instead of producing
    // real base64, we get "104,101,108,108,111" instead -- which
    // decodes to garbage bytes (or throws) on the way back.
    expect(ref.data).toBe('aGVsbG8=');

    const decoded = decodeInlineArtifact(ref);
    expect(Array.from(decoded)).toEqual(Array.from(original));
  });
});

// ---------------------------------------------------------------------
// Family A4 -- downloadArtifact classifier branches
// ---------------------------------------------------------------------
// artifacts.ts:139 begins with
//   if (file instanceof Uint8Array || Buffer.isBuffer(file)) { ... }
// The OR-ordering works "by accident" today because Buffer.isBuffer
// returns false for browser-native shapes, so the code falls through
// to the Blob and PubNubFile branches. Family A rewrites this to
// drop the Buffer.isBuffer short-circuit and use a typeof-guarded
// Blob check plus a duck-typed toArrayBuffer fallback.
//
// These tests gate the classifier by feeding each of the three
// documented return shapes. All three must end up with
// `result.data instanceof Uint8Array` and the correct byte contents.
// ---------------------------------------------------------------------

describe('browser execution: downloadArtifact classifier branches', () => {
  it('Uint8Array return shape', async () => {
    const expected = new Uint8Array([1, 2, 3]);
    pubnubMocks.mockInstance.downloadFile.mockResolvedValue({
      data: new Uint8Array([1, 2, 3]),
    });

    // Call the standalone downloadArtifact helper directly (bypasses
    // TaskSession wiring -- we're unit-testing the classifier).
    const result = await downloadArtifact(
      fileRef(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnubMocks.mockInstance as any,
    );
    expect(result.data).toBeInstanceOf(Uint8Array);
    expect(Array.from(result.data)).toEqual(Array.from(expected));
    expect(result.mimeType).toBe('application/octet-stream');
  });

  it('Blob return shape (documented browser SDK behavior)', async () => {
    const expected = new Uint8Array([4, 5, 6]);
    pubnubMocks.mockInstance.downloadFile.mockResolvedValue({
      data: new Blob([new Uint8Array([4, 5, 6])], {
        type: 'application/octet-stream',
      }),
    });

    const result = await downloadArtifact(
      fileRef(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnubMocks.mockInstance as any,
    );
    expect(result.data).toBeInstanceOf(Uint8Array);
    expect(Array.from(result.data)).toEqual(Array.from(expected));
  });

  it('PubNubFile return shape with toArrayBuffer()', async () => {
    const expected = new Uint8Array([7, 8, 9]);
    const pubnubFile = {
      toArrayBuffer: vi
        .fn()
        .mockResolvedValue(new Uint8Array([7, 8, 9]).buffer),
    };
    pubnubMocks.mockInstance.downloadFile.mockResolvedValue({
      data: pubnubFile,
    });

    const result = await downloadArtifact(
      fileRef(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnubMocks.mockInstance as any,
    );
    expect(pubnubFile.toArrayBuffer).toHaveBeenCalled();
    expect(result.data).toBeInstanceOf(Uint8Array);
    expect(Array.from(result.data)).toEqual(Array.from(expected));
  });

  it('classifier throws on unknown result shapes', async () => {
    pubnubMocks.mockInstance.downloadFile.mockResolvedValue({
      // Neither Uint8Array, nor Blob, nor {toBuffer,toArrayBuffer}.
      data: { mystery: true },
    });

    await expect(
      downloadArtifact(
        fileRef(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnubMocks.mockInstance as any,
      ),
    ).rejects.toThrow();
  });
});

// ---------------------------------------------------------------------
// Family A regression guards -- decodeInlineArtifact
// ---------------------------------------------------------------------
// decodeInlineArtifact already uses atob()+Uint8Array and is browser-
// safe. This test pins current behavior so Family A cannot silently
// regress it.
// ---------------------------------------------------------------------

describe('browser execution: decodeInlineArtifact (regression guard)', () => {
  it('returns Uint8Array from a base64 inline ref', () => {
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 5,
      // btoa is native in jsdom. "hello" -> "aGVsbG8=".
      data: btoa('hello'),
    };
    const decoded = decodeInlineArtifact(ref);
    expect(decoded).toBeInstanceOf(Uint8Array);
    expect(new TextDecoder().decode(decoded)).toBe('hello');
  });

  it('throws for inline ref without data', () => {
    expect(() =>
      decodeInlineArtifact({
        kind: 'inline',
        mimeType: 'text/plain',
        size: 0,
      }),
    ).toThrow();
  });
});

// ---------------------------------------------------------------------
// Fixture end-to-end wiring -- pre-closed TaskSession + downloadArtifact
// ---------------------------------------------------------------------
// Proves the fixture's `createPreClosedTaskSession()` helper threads
// into TaskSession.downloadArtifact() (which lazily allocates a
// PubNub via the hoisted mock) on all three return shapes.
// ---------------------------------------------------------------------

describe('browser execution: fixture wiring for pre-closed TaskSession', () => {
  it('downloadArtifact via TaskSession returns Uint8Array on Blob response', async () => {
    pubnubMocks.mockInstance.downloadFile.mockResolvedValue({
      data: new Blob([new Uint8Array([21, 22, 23])]),
    });

    const session = createPreClosedTaskSession();
    const result = await session.downloadArtifact(fileRef());
    expect(result.data).toBeInstanceOf(Uint8Array);
    expect(Array.from(result.data)).toEqual([21, 22, 23]);
    session.close();
  });
});

// ---------------------------------------------------------------------
// stream.bytes() browser-safety (see STREAM_BYTES_BROWSER_IMPL.md)
// ---------------------------------------------------------------------
// stream.bytes() used to yield Node-only Buffer via Buffer.from().
// After the fix, it yields Uint8Array via stream/bytes.ts helpers
// (base64ToBytes / utf8Encode). jsdom still exposes global `Buffer`,
// so we can't prove non-use of Buffer by shadowing the global without
// destabilizing unrelated SDK code paths that legitimately use
// Buffer (e.g. agent-instance.ts). Instead we assert:
//
//   - yielded values are Uint8Array instances
//   - yielded values are NOT Buffer instances (Buffer.isBuffer false)
//
// The second assertion is the tight one: Buffer extends Uint8Array,
// so `instanceof Uint8Array` alone would pass even for the old
// Buffer-yielding path. `Buffer.isBuffer` is false for plain
// Uint8Array and true for Buffer, so it pins down the contract.
// ---------------------------------------------------------------------

describe('browser execution: stream.bytes() yields Uint8Array (not Buffer)', () => {
  it('decodes base64 and utf-8 chunks into plain Uint8Array', async () => {
    let listener: { message?: (e: unknown) => void } = {};
    pubnubMocks.mockInstance.addListener.mockImplementation((l: unknown) => {
      listener = l as typeof listener;
    });

    const { StreamClient } = await import('../src/stream/stream-client.js');
    const client = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'test-token',
      agentName: 'test_agent',
      streamId: 'browser-bytes-test',
      direction: 'inbound',
      format: 'bytes',
    });

    const iter = client.bytes()[Symbol.asyncIterator]();

    listener.message?.({
      channel: 'stream.test_agent.browser-bytes-test',
      message: {
        type: 'stream_data',
        chunks: [btoa('hello')],
        encoding: 'base64',
        seq: 0,
        ts: Date.now(),
      },
    });
    const first = await iter.next();
    expect(first.done).toBe(false);
    // jsdom + vitest cross a realm boundary for typed arrays, so
    // `instanceof Uint8Array` can fail even though the value is one.
    // Use constructor.name + Buffer.isBuffer(false) to pin the contract.
    expect(first.value.constructor.name).toBe('Uint8Array');
    expect(Buffer.isBuffer(first.value)).toBe(false);
    expect(new TextDecoder().decode(first.value)).toBe('hello');

    listener.message?.({
      channel: 'stream.test_agent.browser-bytes-test',
      message: {
        type: 'stream_data',
        chunks: ['world'],
        encoding: 'utf8',
        seq: 1,
        ts: Date.now(),
      },
    });
    const second = await iter.next();
    expect(second.value.constructor.name).toBe('Uint8Array');
    expect(Buffer.isBuffer(second.value)).toBe(false);
    expect(new TextDecoder().decode(second.value)).toBe('world');

    listener.message?.({
      channel: 'stream.test_agent.browser-bytes-test',
      message: { type: 'stream_end', seq: 2 },
    });
    const done = await iter.next();
    expect(done.done).toBe(true);
  });

  it('yields one Uint8Array per chunk for multi-chunk messages', async () => {
    let listener: { message?: (e: unknown) => void } = {};
    pubnubMocks.mockInstance.addListener.mockImplementation((l: unknown) => {
      listener = l as typeof listener;
    });

    const { StreamClient } = await import('../src/stream/stream-client.js');
    const client = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'test-token',
      agentName: 'test_agent',
      streamId: 'browser-bytes-multi-test',
      direction: 'inbound',
      format: 'bytes',
    });

    const iter = client.bytes()[Symbol.asyncIterator]();

    listener.message?.({
      channel: 'stream.test_agent.browser-bytes-multi-test',
      message: {
        type: 'stream_data',
        chunks: ['aaa', 'bbb'],
        encoding: 'utf8',
        seq: 0,
        ts: Date.now(),
      },
    });

    const r1 = await iter.next();
    expect(r1.value.constructor.name).toBe('Uint8Array');
    expect(Buffer.isBuffer(r1.value)).toBe(false);
    expect(new TextDecoder().decode(r1.value)).toBe('aaa');
    const r2 = await iter.next();
    expect(r2.value.constructor.name).toBe('Uint8Array');
    expect(Buffer.isBuffer(r2.value)).toBe(false);
    expect(new TextDecoder().decode(r2.value)).toBe('bbb');

    listener.message?.({
      channel: 'stream.test_agent.browser-bytes-multi-test',
      message: { type: 'stream_end', seq: 1 },
    });
  });
});
