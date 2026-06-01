import { describe, it, expect, vi } from 'vitest';
import {
  createStreamObject,
  createExternalStreamObject,
  runOnActivate,
} from '../src/runtime/stream-context.js';
import type { StreamError } from '../src/stream/index.js';

/**
 * Build a fake StreamClient whose forwarded members are vi.fn() spies.
 * Each spy returns a unique sentinel so wrapper return-value identity
 * can be asserted with `toBe`. The error-callback list is exposed so a
 * test can fire a synthetic error through it.
 */
function makeFakeClient() {
  const errorCallbacks: Array<(err: StreamError) => void> = [];
  const bytesSentinel = { __sentinel: 'bytes' } as unknown as AsyncIterable<Uint8Array>;
  const eventsSentinel = { __sentinel: 'events' } as unknown as AsyncIterable<unknown>;
  // Use a thenable-like sentinel rather than a real Promise: vitest's
  // toBe identity-comparison for real Promises can resolve through them.
  const readableSentinel = { __sentinel: 'readable' } as unknown as Promise<import('node:stream').Readable>;

  const client = {
    isActive: true,
    channel: 'stream.echo.test',
    uuid: 'echo-stream-0001',
    write: vi.fn(),
    end: vi.fn(async () => {}),
    onEnd: vi.fn(),
    inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
    bytes: vi.fn(() => bytesSentinel),
    events: vi.fn(() => eventsSentinel),
    readable: vi.fn(() => readableSentinel),
    onError: vi.fn((cb: (err: StreamError) => void) => { errorCallbacks.push(cb); }),
  };

  return { client, errorCallbacks, bytesSentinel, eventsSentinel, readableSentinel };
}

describe('stream-context', () => {
  describe('createStreamObject', () => {
    it('wraps a StreamClient correctly', () => {
      const mockClient = {
        isActive: true,
        channel: 'stream.echo.test',
        uuid: 'echo-stream-0001',
        write: vi.fn(),
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
      };

      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const obj = createStreamObject('test', mockClient as any);
      expect(obj.streamId).toBe('test');
      expect(obj.channel).toBe('stream.echo.test');
      expect(obj.isActive).toBe(true);
      expect(obj.external).toBe(false);

      obj.write('hello');
      expect(mockClient.write).toHaveBeenCalledWith('hello');
    });

    // -- Forwarded surface --------------------------------------
    //
    // These tests assert pure delegation only. Decoding correctness for
    // bytes() / events() / readable() lives in the StreamClient tests; we
    // do not re-test it here.

    it('uuid forwards to client.uuid', () => {
      const { client } = makeFakeClient();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const obj = createStreamObject('test', client as any);
      expect(obj.uuid).toBe('echo-stream-0001');
    });

    it('bytes() delegates to client.bytes()', () => {
      const { client, bytesSentinel } = makeFakeClient();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const obj = createStreamObject('test', client as any);
      const result = obj.bytes();
      expect(client.bytes).toHaveBeenCalledOnce();
      expect(result).toBe(bytesSentinel);
    });

    it('events<T>() delegates to client.events()', () => {
      const { client, eventsSentinel } = makeFakeClient();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const obj = createStreamObject('test', client as any);
      const result = obj.events<{ kind: string }>();
      expect(client.events).toHaveBeenCalledOnce();
      expect(result).toBe(eventsSentinel);
    });

    it('readable() delegates to client.readable()', () => {
      const { client, readableSentinel } = makeFakeClient();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const obj = createStreamObject('test', client as any);
      const result = obj.readable();
      expect(client.readable).toHaveBeenCalledOnce();
      expect(result).toBe(readableSentinel);
    });

    /**
     * onError(cb) registers via client.onError. Late-register replay is
     * NOT asserted: StreamClient.onError() (stream-client.ts:429) only
     * appends to the callback list; it does not buffer past errors.
     * Handlers MUST register `onError` before the read path activates,
     * otherwise errors fired before registration are lost.
     */
    it('onError(cb) delegates to client.onError and fires when an error is dispatched', () => {
      const { client, errorCallbacks } = makeFakeClient();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const obj = createStreamObject('test', client as any);
      const cb = vi.fn();

      obj.onError(cb);
      expect(client.onError).toHaveBeenCalledOnce();
      expect(client.onError).toHaveBeenCalledWith(cb);

      const err: StreamError = {
        category: 'access_denied',
        error: { message: 'denied' },
        channel: 'stream.echo.test',
        timestamp: Date.now(),
        fatal: true,
      };
      // Fire through the fake's callback list (mirrors StreamClient.fireError).
      for (const registered of errorCallbacks) registered(err);
      expect(cb).toHaveBeenCalledOnce();
      expect(cb).toHaveBeenCalledWith(err);
    });
  });

  describe('createExternalStreamObject', () => {
    it('exposes token and activate', () => {
      const activateFn = vi.fn(async () => {});
      const obj = createExternalStreamObject('ext-1', 'stream.echo.ext-1', 't7a-token', activateFn);

      expect(obj.streamId).toBe('ext-1');
      expect(obj.channel).toBe('stream.echo.ext-1');
      expect(obj.external).toBe(true);
      expect(obj.token).toBe('t7a-token');
      expect(typeof obj.activate).toBe('function');
    });

    it('write throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.write('data')).toThrow('external');
    });

    it('inbound throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.inbound).toThrow('external');
    });

    // -- Forwarded surface throws on external -------------------

    it('uuid throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.uuid).toThrow(/external stream/);
    });

    it('bytes() throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.bytes()).toThrow(/external stream/);
    });

    it('events() throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.events()).toThrow(/external stream/);
    });

    it('readable() throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.readable()).toThrow(/external stream/);
    });

    it('onError() throws on external', () => {
      const obj = createExternalStreamObject('ext-1', 'ch', 'tok', async () => {});
      expect(() => obj.onError(() => {})).toThrow(/external stream/);
    });
  });

  describe('runOnActivate', () => {
    it('calls the callback with the stream object', async () => {
      const mockClient = {
        isActive: true,
        channel: 'stream.echo.test',
        uuid: 'echo-stream-0001',
        write: vi.fn(),
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
      };
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const streamObj = createStreamObject('test', mockClient as any);
      const callback = vi.fn();
      const failStream = vi.fn(async () => {});

      await runOnActivate('test', streamObj, callback, failStream);

      expect(callback).toHaveBeenCalledWith(streamObj);
      expect(failStream).not.toHaveBeenCalled();
    });

    it('calls failStream when onActivate throws', async () => {
      const mockClient = {
        isActive: true,
        channel: 'stream.echo.test',
        uuid: 'echo-stream-0001',
        write: vi.fn(),
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
      };
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const streamObj = createStreamObject('test', mockClient as any);
      const callback = vi.fn(() => { throw new Error('boom'); });
      const failStream = vi.fn(async () => {});

      await runOnActivate('test', streamObj, callback, failStream);

      expect(failStream).toHaveBeenCalledWith('test', 'stream_crashed');
    });

    it('handles async onActivate', async () => {
      const mockClient = {
        isActive: true,
        channel: 'ch',
        uuid: 'u',
        write: vi.fn(),
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
      };
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const streamObj = createStreamObject('test', mockClient as any);
      let resolved = false;
      const callback = async () => {
        await new Promise((r) => setTimeout(r, 10));
        resolved = true;
      };
      const failStream = vi.fn(async () => {});

      await runOnActivate('test', streamObj, callback, failStream);

      expect(resolved).toBe(true);
      expect(failStream).not.toHaveBeenCalled();
    });
  });
});
