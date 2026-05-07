import { describe, it, expect, beforeEach } from 'vitest';
import { CredentialCache } from '../src/runtime/credential-cache.js';

describe('CredentialCache', () => {
  let cache: CredentialCache;

  beforeEach(() => {
    cache = new CredentialCache();
  });

  it('stores and retrieves credentials', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    const creds = cache.get('task-1');
    expect(creds).toBeDefined();
    expect(creds!.ownerId).toBe('alice');
    expect(creds!.writeToken).toBe('tok-1');
    expect(creds!.agentName).toBe('echo');
    expect(creds!.streamIds.size).toBe(0);
  });

  it('returns undefined for unknown task', () => {
    expect(cache.get('nonexistent')).toBeUndefined();
  });

  it('adds stream IDs to a task entry', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    cache.addStream('task-1', 'stream-a');
    cache.addStream('task-1', 'stream-b');
    const creds = cache.get('task-1');
    expect(creds!.streamIds.has('stream-a')).toBe(true);
    expect(creds!.streamIds.has('stream-b')).toBe(true);
    expect(creds!.streamIds.size).toBe(2);
  });

  it('addStream is a no-op for unknown task', () => {
    cache.addStream('unknown', 'stream-a');
    expect(cache.get('unknown')).toBeUndefined();
  });

  it('removes a task entry', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    cache.remove('task-1');
    expect(cache.has('task-1')).toBe(false);
    expect(cache.get('task-1')).toBeUndefined();
  });

  it('has returns correct boolean', () => {
    expect(cache.has('task-1')).toBe(false);
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    expect(cache.has('task-1')).toBe(true);
  });

  it('taskIds returns all keys', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    cache.set('task-2', { ownerId: 'bob', orgId: 'bob', writeToken: 'tok-2', agentName: 'echo' });
    expect(cache.taskIds().sort()).toEqual(['task-1', 'task-2']);
  });

  it('clear removes all entries', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    cache.set('task-2', { ownerId: 'bob', orgId: 'bob', writeToken: 'tok-2', agentName: 'echo' });
    cache.clear();
    expect(cache.taskIds()).toEqual([]);
    expect(cache.has('task-1')).toBe(false);
  });

  it('updates existing entry on second set call', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'alice', writeToken: 'tok-1', agentName: 'echo' });
    cache.addStream('task-1', 'stream-a');
    cache.set('task-1', { ownerId: 'bob', orgId: 'bob', writeToken: 'tok-2', agentName: 'video' });
    const creds = cache.get('task-1');
    expect(creds!.ownerId).toBe('bob');
    expect(creds!.writeToken).toBe('tok-2');
    // Stream IDs preserved on update
    expect(creds!.streamIds.has('stream-a')).toBe(true);
  });

  it('stores and retrieves orgId separately from ownerId', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'acme-corp', writeToken: 'tok-1', agentName: 'echo' });
    const creds = cache.get('task-1');
    expect(creds!.ownerId).toBe('alice');
    expect(creds!.orgId).toBe('acme-corp');
  });

  it('updates orgId on second set call', () => {
    cache.set('task-1', { ownerId: 'alice', orgId: 'org-a', writeToken: 'tok-1', agentName: 'echo' });
    cache.set('task-1', { ownerId: 'alice', orgId: 'org-b', writeToken: 'tok-1', agentName: 'echo' });
    const creds = cache.get('task-1');
    expect(creds!.orgId).toBe('org-b');
  });
});
