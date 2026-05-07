import { describe, it, expect, beforeEach } from 'vitest';
import { StreamRegistry } from '../src/runtime/stream-registry.js';

describe('StreamRegistry', () => {
  let registry: StreamRegistry;

  beforeEach(() => {
    registry = new StreamRegistry();
  });

  describe('acquire', () => {
    it('creates a new entry for unknown stream', () => {
      const { entry, isNew, isNewForTask } = registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      expect(isNew).toBe(true);
      expect(isNewForTask).toBe(true);
      expect(entry.streamId).toBe('stream-1');
      expect(entry.refCount).toBe(1);
      expect(entry.taskIds.has('task-1')).toBe(true);
      expect(entry.direction).toBe('outbound');
      expect(entry.format).toBe('bytes');
      expect(entry.external).toBe(false);
      // Default affinity is 'dedicated' when not supplied.
      expect(entry.affinity).toBe('dedicated');
    });

    it('captures affinity at first acquire', () => {
      const { entry } = registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false, affinity: 'shared',
      });
      expect(entry.affinity).toBe('shared');
    });

    it('reuses existing entry with matching config for a new task', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      const { entry, isNew, isNewForTask } = registry.acquire('stream-1', 'task-2', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      expect(isNew).toBe(false);
      expect(isNewForTask).toBe(true);
      expect(entry.refCount).toBe(2);
      expect(entry.taskIds.has('task-1')).toBe(true);
      expect(entry.taskIds.has('task-2')).toBe(true);
    });

    it('is idempotent when the same task re-acquires (no taskIds growth)', () => {
      const first = registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      const second = registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      expect(second.isNew).toBe(false);
      expect(second.isNewForTask).toBe(false);
      expect(second.entry).toBe(first.entry);
      expect(second.entry.refCount).toBe(1);
      expect(second.entry.taskIds.size).toBe(1);
    });

    it('rejects direction mismatch', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      expect(() => {
        registry.acquire('stream-1', 'task-2', {
          direction: 'inbound', format: 'bytes', external: false,
        });
      }).toThrow('direction mismatch');
    });

    it('rejects format mismatch', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      expect(() => {
        registry.acquire('stream-1', 'task-2', {
          direction: 'outbound', format: 'events', external: false,
        });
      }).toThrow('format mismatch');
    });

    it('rejects external flag mismatch', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      expect(() => {
        registry.acquire('stream-1', 'task-2', {
          direction: 'outbound', format: 'bytes', external: true,
        });
      }).toThrow('cannot mix embedded and external');
    });

    it('rejects affinity mismatch', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false, affinity: 'shared',
      });
      expect(() => {
        registry.acquire('stream-1', 'task-2', {
          direction: 'outbound', format: 'bytes', external: false, affinity: 'dedicated',
        });
      }).toThrow('affinity mismatch');
    });
  });

  describe('release', () => {
    it('decrements refCount and removes at 0', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      const remaining = registry.release('stream-1', 'task-1');
      expect(remaining).toBe(0);
      expect(registry.get('stream-1')).toBeUndefined();
    });

    it('preserves entry when refCount > 0', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.acquire('stream-1', 'task-2', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      const remaining = registry.release('stream-1', 'task-1');
      expect(remaining).toBe(1);
      const entry = registry.get('stream-1');
      expect(entry).toBeDefined();
      expect(entry!.taskIds.has('task-1')).toBe(false);
      expect(entry!.taskIds.has('task-2')).toBe(true);
    });

    it('returns 0 for unknown stream', () => {
      expect(registry.release('unknown', 'task-1')).toBe(0);
    });
  });

  describe('forceRemove', () => {
    it('removes entry from registry and returns it for terminal-fan-out', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.acquire('stream-1', 'task-2', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      const entry = registry.forceRemove('stream-1');
      expect(entry).toBeDefined();
      // taskIds is preserved so failStream can iterate the set of tasks
      // to publish terminal failure to. The registry no longer tracks it.
      expect(entry!.taskIds.has('task-1')).toBe(true);
      expect(entry!.taskIds.has('task-2')).toBe(true);
      expect(registry.get('stream-1')).toBeUndefined();
    });

    it('returns undefined for unknown stream', () => {
      expect(registry.forceRemove('unknown')).toBeUndefined();
    });
  });

  describe('releaseAllForTask', () => {
    it('releases all streams for a task', () => {
      registry.acquire('stream-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.acquire('stream-2', 'task-1', {
        direction: 'inbound', format: 'events', external: false,
      });
      const destroyed = registry.releaseAllForTask('task-1');
      expect(destroyed.map(e => e.streamId).sort()).toEqual(['stream-1', 'stream-2']);
      expect(registry.get('stream-1')).toBeUndefined();
      expect(registry.get('stream-2')).toBeUndefined();
    });

    it('only destroys streams that reach refCount 0', () => {
      registry.acquire('shared', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.acquire('shared', 'task-2', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      const destroyed = registry.releaseAllForTask('task-1');
      expect(destroyed).toEqual([]);
      expect(registry.get('shared')!.refCount).toBe(1);
    });
  });

  describe('activeStreamCount', () => {
    it('counts non-external streams', () => {
      registry.acquire('embedded-1', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.acquire('external-1', 'task-2', {
        direction: 'outbound', format: 'bytes', external: true,
      });
      expect(registry.activeStreamCount).toBe(1);
    });
  });

  describe('streamIds', () => {
    it('returns all stream IDs', () => {
      registry.acquire('stream-a', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.acquire('stream-b', 'task-1', {
        direction: 'inbound', format: 'events', external: false,
      });
      expect(registry.streamIds().sort()).toEqual(['stream-a', 'stream-b']);
    });
  });

  describe('clear', () => {
    it('removes all entries', () => {
      registry.acquire('stream-a', 'task-1', {
        direction: 'outbound', format: 'bytes', external: false,
      });
      registry.clear();
      expect(registry.streamIds()).toEqual([]);
    });
  });
});
