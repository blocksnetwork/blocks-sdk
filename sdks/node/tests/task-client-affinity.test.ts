import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { TaskClient } from '../src/runtime/task-client.js';
import { captureAffinity, resetAffinity } from '../src/runtime/write-affinity.js';

const BASE_URL = 'http://localhost:3001';

function tokenResponse() {
  return {
    ok: true,
    status: 200,
    headers: new Headers(),
    json: async () => ({ pamToken: 'pam-x', channel: 'u.org.t', ttlMinutes: 5 }),
    text: async () => '',
  };
}

describe('TaskClient.fetchConsumerReadToken write-affinity wiring', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    resetAffinity();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    resetAffinity();
  });

  it('preserves prior affinity across token calls: backend never emits the header here', async () => {
    const future = String(Math.floor(Date.now() / 1000) + 60);
    captureAffinity(new Headers({ 'x-write-affinity': future }));

    fetchSpy
      .mockResolvedValueOnce(tokenResponse())
      .mockResolvedValueOnce(tokenResponse());

    const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: BASE_URL });
    const fetchReadToken = (
      client as unknown as { fetchConsumerReadToken: (id: string) => Promise<unknown> }
    ).fetchConsumerReadToken.bind(client);

    await fetchReadToken('task-1');
    await fetchReadToken('task-2');

    expect(fetchSpy).toHaveBeenCalledTimes(2);
    const firstHeaders = fetchSpy.mock.calls[0][1].headers as Record<string, string>;
    expect(firstHeaders['x-write-affinity']).toBe(future);
    const secondHeaders = fetchSpy.mock.calls[1][1].headers as Record<string, string>;
    expect(secondHeaders['x-write-affinity']).toBe(future);
  });
});
