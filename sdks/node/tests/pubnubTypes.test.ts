import { describe, expect, it } from 'vitest';
import {
  asPayload,
  asPayloadMeta,
  toPayload,
  fromPayload,
  asPubNubFetcher,
  supportsMessageActions,
  supportsFetchMessages,
  type JsonObject,
} from '../src/runtime/pubnub-types.js';

describe('pubnubTypes module', () => {
  describe('asPayload', () => {
    it('converts object to Payload type', () => {
      const obj = { type: 'progress', taskId: 'task-1', progress: 0.5 };
      const payload = asPayload(obj);
      expect(payload).toBe(obj);
    });

    it('handles nested objects', () => {
      const obj = {
        type: 'artifact',
        taskId: 'task-1',
        data: { nested: { deep: 'value' } },
      };
      const payload = asPayload(obj);
      expect(payload).toBe(obj);
    });

    it('handles arrays in objects', () => {
      const obj = { items: [1, 2, 3], tags: ['a', 'b'] };
      const payload = asPayload(obj);
      expect(payload).toBe(obj);
    });
  });

  describe('asPayloadMeta', () => {
    it('converts object to Payload for meta field', () => {
      const obj = { taskId: 'task-1', agentName: 'acme-echo' };
      const meta = asPayloadMeta(obj);
      expect(meta).toBe(obj);
    });
  });

  describe('toPayload', () => {
    it('converts typed JsonObject to Payload', () => {
      const obj: JsonObject = {
        type: 'terminal',
        taskId: 'task-1',
        state: 'completed',
      };
      const payload = toPayload(obj);
      expect(payload).toBe(obj);
    });

    it('preserves type information', () => {
      interface MyMessage extends JsonObject {
        type: string;
        taskId: string;
      }
      const obj: MyMessage = { type: 'progress', taskId: 'task-1' };
      const payload = toPayload(obj);
      // Payload is the same object reference
      expect(payload).toBe(obj);
    });
  });

  describe('fromPayload', () => {
    it('casts payload back to typed object', () => {
      const payload: unknown = { type: 'progress', taskId: 'task-1', progress: 0.5 };
      interface ProgressEvent {
        type: string;
        taskId: string;
        progress: number;
      }
      const typed = fromPayload<ProgressEvent>(payload);
      expect(typed.type).toBe('progress');
      expect(typed.taskId).toBe('task-1');
      expect(typed.progress).toBe(0.5);
    });
  });

  describe('asPubNubFetcher', () => {
    it('returns undefined for null', () => {
      expect(asPubNubFetcher(null)).toBeUndefined();
    });

    it('returns undefined for undefined', () => {
      expect(asPubNubFetcher(undefined)).toBeUndefined();
    });

    it('returns undefined for non-object', () => {
      expect(asPubNubFetcher('string')).toBeUndefined();
      expect(asPubNubFetcher(123)).toBeUndefined();
    });

    it('returns undefined when fetchMessages is missing', () => {
      expect(asPubNubFetcher({})).toBeUndefined();
    });

    it('returns undefined when fetchMessages is not a function', () => {
      expect(asPubNubFetcher({ fetchMessages: 'not a function' })).toBeUndefined();
    });

    it('returns the object when fetchMessages is a function', () => {
      const client = { fetchMessages: () => Promise.resolve({ channels: {} }) };
      expect(asPubNubFetcher(client)).toBe(client);
    });
  });

  describe('supportsMessageActions', () => {
    it('returns false when addMessageAction is missing', () => {
      const client = { publish: () => Promise.resolve({ timetoken: '123' }) };
      expect(supportsMessageActions(client as never)).toBe(false);
    });

    it('returns false when addMessageAction is not a function', () => {
      const client = {
        publish: () => Promise.resolve({ timetoken: '123' }),
        addMessageAction: null,
      };
      expect(supportsMessageActions(client as never)).toBe(false);
    });

    it('returns true when addMessageAction is a function', () => {
      const client = {
        publish: () => Promise.resolve({ timetoken: '123' }),
        addMessageAction: () => Promise.resolve({}),
      };
      expect(supportsMessageActions(client as never)).toBe(true);
    });
  });

  describe('supportsFetchMessages', () => {
    it('returns false when fetchMessages is missing', () => {
      const client = { publish: () => Promise.resolve({ timetoken: '123' }) };
      expect(supportsFetchMessages(client as never)).toBe(false);
    });

    it('returns true when fetchMessages is a function', () => {
      const client = {
        publish: () => Promise.resolve({ timetoken: '123' }),
        fetchMessages: () => Promise.resolve({ channels: {} }),
      };
      expect(supportsFetchMessages(client as never)).toBe(true);
    });
  });
});
