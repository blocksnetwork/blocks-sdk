import { describe, it, expect, beforeEach, afterEach, /* afterAll, */ vi } from 'vitest';
// import PubNub from 'pubnub';
// try { await import('dotenv/config'); } catch { /* dotenv not installed — live tests require it */ }
import {
  fetchAgentRegistry,
  connectAgent,
  getAgent,
  removeAgent,
  fetchAgentsBySkill,
  fetchAgentsByListing,
  registryAllChannel,
  registrySkillChannel,
  registryVisibilityChannel,
  registryLogChannel,
  normalizeSkillSlug,
  type AgentCard,
} from '../src/runtime/agent-registry.js';
// import { hasLiveEnv } from './helpers/live-test-config.js';

const TEST_BASE_URL = 'http://test-api.example.com';
const TEST_INSTANCE_ID = 'test-instance-1';

// TODO: Re-enable once live endpoint is reachable (currently returns HTML 404)
// describe.skipIf(process.env.PUBNUB_LIVE_TEST !== '1' || !hasLiveEnv())(
//   'Agent Registry (live)',
//   () => {
//     let pubnub: PubNub;
//     const testRunId = Date.now();
//     const instanceId = `live-test-${testRunId}`;
//     const createdAgentNames: string[] = [];
//
//     afterAll(async () => {
//       for (const agentName of createdAgentNames) {
//         try {
//           await removeAgent(agentName);
//           console.log(`[cleanup] Removed agent name: ${agentName}`);
//         } catch (err) {
//           console.warn(`[cleanup] Failed to remove agent name ${agentName}:`, err);
//         }
//       }
//     });
//
//     beforeEach(() => {
//       pubnub = new PubNub({
//         publishKey: process.env.PUBNUB_PUBLISH_KEY!,
//         subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
//         userId: `test-${testRunId}-${Math.random().toString(36).slice(2)}`,
//         secretKey: process.env.PUBNUB_SECRET_KEY!,
//       });
//     });
//
//     it('registers a new agent name and fetches it back', async () => {
//       const agentName = `test-type-${testRunId}`;
//       createdAgentNames.push(agentName);
//
//       await connectAgent(agentName, {
//         instanceId,
//         description: 'Test agent for automated tests',
//         card: {
//           name: 'Test Agent',
//           description: 'Test agent for automated tests',
//           version: '1.0.0',
//           provider: { organization: 'TestOrg' },
//           defaultInputModes: ['application/json'],
//           defaultOutputModes: ['application/json'],
//           capabilities: { streaming: false },
//           skills: [{ id: 'test', name: 'Test' }, { id: 'automation', name: 'Automation' }],
//           runtime: { agentName, handler: './handler.ts' },
//         },
//       });
//
//       let entry: Awaited<ReturnType<typeof getAgent>> | null = null;
//       for (let attempt = 0; attempt < 5; attempt++) {
//         await new Promise((r) => setTimeout(r, 2000));
//         entry = await getAgent(agentName);
//         if (entry) break;
//       }
//
//       expect(entry).not.toBeNull();
//       expect(entry?.description).toBe('Test agent for automated tests');
//       expect(entry?.skills).toEqual([{ id: 'test', name: 'Test' }, { id: 'automation', name: 'Automation' }]);
//       expect(entry?.listing).toBe('playground');
//     }, 30000);
//
//     it('registers agent with full card payload', async () => {
//       const agentName = `card-test-${testRunId}`;
//       createdAgentNames.push(agentName);
//       const card: AgentCard = {
//         name: 'Test Card Agent',
//         description: 'Agent with full card',
//         version: '1.0.0',
//         provider: { organization: 'TestOrg' },
//         defaultInputModes: ['application/json'],
//         defaultOutputModes: ['application/json'],
//         capabilities: { streaming: false },
//         skills: [{ id: 'test-skill', name: 'Test Skill', description: 'A test skill' }],
//       };
//
//       await connectAgent(agentName, { instanceId, card });
//
//       await new Promise((r) => setTimeout(r, 1500));
//
//       const entry = await getAgent(agentName);
//       expect(entry).not.toBeNull();
//       expect(entry?.card?.name).toBe('Test Card Agent');
//       expect(entry?.card?.skills).toHaveLength(1);
//       expect(entry?.card?.skills[0].id).toBe('test-skill');
//     }, 15000);
//
//     it('filters agents by skill', async () => {
//       const agentName = `skill-test-${testRunId}`;
//       createdAgentNames.push(agentName);
//       await connectAgent(agentName, {
//         instanceId,
//         card: {
//           name: 'Skill Test Agent',
//           description: 'Skill test',
//           version: '1.0.0',
//           provider: { organization: 'TestOrg' },
//           defaultInputModes: ['application/json'],
//           defaultOutputModes: ['application/json'],
//           capabilities: { streaming: false },
//           skills: [{ id: 'image-generation', name: 'Image Generation' }, { id: 'text.embeddings', name: 'Text Embeddings' }],
//         },
//       });
//
//       let entry: Awaited<ReturnType<typeof getAgent>> | null = null;
//       for (let attempt = 0; attempt < 5; attempt++) {
//         await new Promise((r) => setTimeout(r, 2000));
//         entry = await getAgent(agentName);
//         if (entry) break;
//       }
//
//       expect(entry).not.toBeNull();
//       expect(entry?.skills).toEqual(
//         expect.arrayContaining([
//           expect.objectContaining({ id: 'image-generation' }),
//           expect.objectContaining({ id: 'text.embeddings' }),
//         ]),
//       );
//     }, 30000);
//
//     it('filters agents by listing', async () => {
//       const publicAgent = `public-${testRunId}`;
//       const privateAgent = `private-${testRunId}`;
//       createdAgentNames.push(publicAgent, privateAgent);
//
//       await connectAgent(publicAgent, { instanceId, listing: 'public' });
//       await connectAgent(privateAgent, { instanceId, listing: 'private' });
//
//       let foundPublic: Awaited<ReturnType<typeof getAgent>> | null = null;
//       let foundPrivate: Awaited<ReturnType<typeof getAgent>> | null = null;
//       for (let attempt = 0; attempt < 5; attempt++) {
//         await new Promise((r) => setTimeout(r, 2000));
//         if (!foundPublic) foundPublic = await getAgent(publicAgent);
//         if (!foundPrivate) foundPrivate = await getAgent(privateAgent);
//         if (foundPublic && foundPrivate) break;
//       }
//
//       expect(foundPublic).not.toBeNull();
//       expect(foundPublic?.listing).toBe('public');
//       expect(foundPrivate).not.toBeNull();
//       expect(foundPrivate?.listing).toBe('private');
//     }, 30000);
//
//     it('removes an agent from the registry', async () => {
//       const agentName = `remove-test-${testRunId}`;
//       await connectAgent(agentName, {
//         instanceId,
//         description: 'To be removed',
//       });
//
//       let beforeRemove: Awaited<ReturnType<typeof getAgent>> | null = null;
//       for (let attempt = 0; attempt < 5; attempt++) {
//         await new Promise((r) => setTimeout(r, 2000));
//         beforeRemove = await getAgent(agentName);
//         if (beforeRemove) break;
//       }
//       expect(beforeRemove).not.toBeNull();
//
//       const removed = await removeAgent(agentName);
//       expect(removed).toBe(true);
//
//       await new Promise((r) => setTimeout(r, 1500));
//
//       const afterRemove = await getAgent(agentName);
//       expect(afterRemove).toBeNull();
//     }, 15000);
//
//     it('subscribes to audit events on registry.log', async () => {
//       const agentName = `audit-test-${testRunId}`;
//       createdAgentNames.push(agentName);
//       const auditEvents: unknown[] = [];
//
//       const listener = {
//         message: (event: { channel: string; message: unknown }) => {
//           if (event.channel === registryLogChannel()) {
//             auditEvents.push(event.message);
//           }
//         },
//       };
//
//       pubnub.addListener(listener);
//       pubnub.subscribe({ channels: [registryLogChannel()] });
//
//       await new Promise((r) => setTimeout(r, 1000));
//
//       await connectAgent(agentName, {
//         instanceId,
//         card: {
//           name: 'Audit Test Agent',
//           description: 'Audit test',
//           version: '1.0.0',
//           provider: { organization: 'TestOrg' },
//           defaultInputModes: ['application/json'],
//           defaultOutputModes: ['application/json'],
//           capabilities: { streaming: false },
//           skills: [{ id: 'audit-test', name: 'Audit Test' }],
//         },
//       });
//
//       await new Promise((r) => setTimeout(r, 2000));
//
//       pubnub.removeListener(listener);
//       pubnub.unsubscribe({ channels: [registryLogChannel()] });
//
//       // Server-side handler publishes audit events; we may or may not receive them
//       // depending on timing, so just verify no error was thrown during registration
//     }, 15000);
//   },
// );

// Unit tests that don't require live PubNub
describe('Agent Registry (unit)', () => {
  it('exports registry channel helpers', () => {
    expect(registryAllChannel()).toBe('registry.all');
    expect(registrySkillChannel('test')).toBe('registry.skill.test');
    expect(registryVisibilityChannel(true)).toBe('registry.public');
    expect(registryVisibilityChannel(false)).toBe('registry.private');
    expect(registryLogChannel()).toBe('registry.log');
  });

  it('normalizes skill slugs correctly', () => {
    expect(normalizeSkillSlug('image-generation')).toBe('image_generation');
    expect(normalizeSkillSlug('text.embeddings')).toBe('text.embeddings');
    expect(normalizeSkillSlug('Image Generation')).toBe('image_generation');
  });

  describe('connectAgent (REST)', () => {
    function mockAgentAuth(response = { pamToken: 'pam-test', accessToken: 'jwt-1', refreshToken: 'rt-1' }) {
      return { init: vi.fn().mockResolvedValue(response) };
    }

    it('throws when agentName contains a dot', async () => {
      await expect(
        connectAgent('acme.echo', {
          instanceId: TEST_INSTANCE_ID,
          baseUrl: TEST_BASE_URL,
          agentAuth: mockAgentAuth() as any,
        }),
      ).rejects.toThrow('agentName must contain only alphanumeric characters and underscores (no hyphens)');
    });

    it('throws when agentAuth is not provided', async () => {
      await expect(
        connectAgent('test_agent', {
          instanceId: TEST_INSTANCE_ID,
          baseUrl: TEST_BASE_URL,
        }),
      ).rejects.toThrow('agentAuth is required');
    });

    it('sends correct connect payload via agentAuth.init', async () => {
      const auth = mockAgentAuth();

      await connectAgent('test_agent', {
        instanceId: TEST_INSTANCE_ID,
        baseUrl: TEST_BASE_URL,
        description: 'Test agent',
        scaling: { expectedInstances: 3, concurrency: 4 },
        agentAuth: auth as any,
      });

      expect(auth.init).toHaveBeenCalledTimes(1);
      const payload = auth.init.mock.calls[0][0];
      expect(payload.agentName).toBe('test_agent');
      expect(payload.instanceId).toBe(TEST_INSTANCE_ID);
      expect(payload.description).toBeUndefined();
      expect(payload.expectedInstances).toBe(3);
      expect(payload.concurrency).toBe(4);
    });

    it('does not send card, cardRef, or cardSummary in connect payload', async () => {
      const auth = mockAgentAuth();

      const card: AgentCard = {
        identity: {
          agentName: 'test_agent',
          displayName: 'Test',
          description: 'Test',
          version: '1.0',
          provider: { organization: 'TestOrg' },
        },
        capabilities: { taskKinds: ['request'] },
        skills: [{ id: 'test', name: 'Test' }],
      };

      await connectAgent('test_agent', {
        instanceId: TEST_INSTANCE_ID,
        baseUrl: TEST_BASE_URL,
        card,
        cardRef: 'ref-123',
        cardSummary: 'A test summary',
        agentAuth: auth as any,
      });

      const payload = auth.init.mock.calls[0][0];
      expect(payload.card).toBeUndefined();
      expect(payload.cardRef).toBeUndefined();
      expect(payload.cardSummary).toBeUndefined();
    });

    it('omits listing when not specified', async () => {
      const auth = mockAgentAuth();

      await connectAgent('test_agent', {
        instanceId: TEST_INSTANCE_ID,
        baseUrl: TEST_BASE_URL,
        agentAuth: auth as any,
      });

      const payload = auth.init.mock.calls[0][0];
      expect(payload.listing).toBeUndefined();
    });

    it('passes listing: private when explicitly set', async () => {
      const auth = mockAgentAuth();

      await connectAgent('test_agent', {
        instanceId: TEST_INSTANCE_ID,
        baseUrl: TEST_BASE_URL,
        listing: 'private',
        agentAuth: auth as any,
      });

      const payload = auth.init.mock.calls[0][0];
      expect(payload.listing).toBe('private');
    });

    it('includes scaling params as expectedInstances and concurrency', async () => {
      const auth = mockAgentAuth();

      await connectAgent('scaling_agent', {
        instanceId: TEST_INSTANCE_ID,
        baseUrl: TEST_BASE_URL,
        scaling: {
          expectedInstances: 2,
          concurrency: 4,
          maxPendingBacklog: 20,
          maxRunningTimeSec: 300,
        },
        agentAuth: auth as any,
      });

      const payload = auth.init.mock.calls[0][0];
      expect(payload.expectedInstances).toBe(2);
      expect(payload.concurrency).toBe(4);
      expect(payload.maxPendingBacklog).toBe(20);
      expect(payload.maxRunningTimeSec).toBe(300);
    });

    it('returns pamToken from agentAuth response', async () => {
      const auth = mockAgentAuth({ pamToken: 'pam-xyz', accessToken: 'jwt', refreshToken: 'rt' });

      const result = await connectAgent('test_agent', {
        instanceId: TEST_INSTANCE_ID,
        baseUrl: TEST_BASE_URL,
        agentAuth: auth as any,
      });

      expect(result.pamToken).toBe('pam-xyz');
    });
  });

  describe('getAgent (REST)', () => {
    let fetchSpy: ReturnType<typeof vi.fn>;
    const originalFetch = globalThis.fetch;

    beforeEach(() => {
      fetchSpy = vi.fn();
      globalThis.fetch = fetchSpy;
    });

    afterEach(() => {
      globalThis.fetch = originalFetch;
    });

    it('fetches a single agent by name', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agent: {
            agentName: 'acme-echo',
            name: 'Echo Agent',
            description: 'An echo agent',
            skills: [{ id: 'echo', name: 'Echo' }],
            listing: 'public',
            card: { name: 'Echo Agent', skills: [{ id: 'echo', name: 'Echo' }] },
            scaling: { expectedInstances: 1, concurrency: 2 },
            registeredAt: '2024-01-01T00:00:00Z',
          },
        }),
      });

      const entry = await getAgent('acme-echo', { baseUrl: TEST_BASE_URL });

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url] = fetchSpy.mock.calls[0];
      expect(url).toBe(`${TEST_BASE_URL}/api/v1/registry/agents?agentName=acme-echo`);

      expect(entry).not.toBeNull();
      expect(entry?.agentName).toBe('acme-echo');
      expect(entry?.displayName).toBe('Echo Agent');
      expect(entry?.description).toBe('An echo agent');
      expect(entry?.skills).toEqual([{ id: 'echo', name: 'Echo' }]);
      expect(entry?.listing).toBe('public');
      expect(entry?.card?.name).toBe('Echo Agent');
      expect(entry?.scaling?.expectedInstances).toBe(1);
      expect(entry?.createdAt).toBe('2024-01-01T00:00:00Z');
    });

    it('propagates billingMode from server response', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agent: {
            agentName: 'paid-agent',
            name: 'Paid Agent',
            listing: 'public',
            billingMode: 'paid',
          },
        }),
      });

      const entry = await getAgent('paid-agent', { baseUrl: TEST_BASE_URL });
      expect(entry?.billingMode).toBe('paid');
      expect(entry?.listing).toBe('public');
    });

    it('defaults listing to public and leaves billingMode undefined when omitted', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agent: {
            agentName: 'bare-agent',
            name: 'Bare Agent',
          },
        }),
      });

      const entry = await getAgent('bare-agent', { baseUrl: TEST_BASE_URL });
      expect(entry?.listing).toBe('public');
      expect(entry?.billingMode).toBeUndefined();
    });

    it('returns null on 404', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 404,
        json: async () => ({ code: 'NotFound', message: 'Agent not found' }),
      });

      const entry = await getAgent('nonexistent', { baseUrl: TEST_BASE_URL });
      expect(entry).toBeNull();
    });

    it('uses custom baseUrl when provided', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agent: { agentName: 'acme-echo', name: 'Echo Agent' },
        }),
      });

      await getAgent('acme-echo', { baseUrl: 'http://localhost:8080' });

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toBe('http://localhost:8080/api/v1/registry/agents?agentName=acme-echo');
    });

    it('sends Authorization: Bearer header when apiKey is provided', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agent: { agentName: 'private-agent', name: 'Private', listing: 'private' },
        }),
      });

      await getAgent('private-agent', {
        baseUrl: TEST_BASE_URL,
        apiKey: 'bk_test_key',
      });

      const [, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
      const headers = new Headers(init.headers);
      expect(headers.get('Authorization')).toBe('Bearer bk_test_key');
    });

    it('omits Authorization header when apiKey is not provided', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agent: { agentName: 'public-agent', name: 'Public' },
        }),
      });

      await getAgent('public-agent', { baseUrl: TEST_BASE_URL });

      const [, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
      const headers = new Headers(init?.headers);
      expect(headers.has('Authorization')).toBe(false);
    });
  });

  describe('fetchAgentRegistry (REST)', () => {
    let fetchSpy: ReturnType<typeof vi.fn>;
    const originalFetch = globalThis.fetch;

    beforeEach(() => {
      fetchSpy = vi.fn();
      globalThis.fetch = fetchSpy;
    });

    afterEach(() => {
      globalThis.fetch = originalFetch;
    });

    it('fetches all agents with include=full', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          agents: [
            {
              agentName: 'agent-1',
              name: 'Agent One',
              description: 'First agent',
              skills: [{ id: 'cap1', name: 'Cap 1' }, { id: 'cap2', name: 'Cap 2' }],
              listing: 'public',
            },
            {
              agentName: 'agent-2',
              name: 'Agent Two',
              description: 'Second agent',
              listing: 'private',
            },
          ],
          totalCount: 2,
          next: null,
        }),
      });

      const result = await fetchAgentRegistry({ baseUrl: TEST_BASE_URL });

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url] = fetchSpy.mock.calls[0];
      expect(url).toContain('include=full');

      expect(result.agents.length).toBe(2);
      expect(result.totalCount).toBe(2);

      const agent1 = result.agents.find((a) => a.agentName === 'agent-1');
      expect(agent1?.displayName).toBe('Agent One');
      expect(agent1?.description).toBe('First agent');
      expect(agent1?.skills).toEqual([{ id: 'cap1', name: 'Cap 1' }, { id: 'cap2', name: 'Cap 2' }]);
      expect(agent1?.listing).toBe('public');

      const agent2 = result.agents.find((a) => a.agentName === 'agent-2');
      expect(agent2?.displayName).toBe('Agent Two');
      expect(agent2?.listing).toBe('private');
    });

    it('returns empty registry on 404', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 404,
        json: async () => ({ code: 'NotFound' }),
      });

      const result = await fetchAgentRegistry({ baseUrl: TEST_BASE_URL });
      expect(result).toEqual({ agents: [], totalCount: 0 });
    });

    it('passes limit and cursor as query params', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ agents: [], totalCount: 0, next: null }),
      });

      await fetchAgentRegistry({ limit: 25, cursor: 'abc123', baseUrl: TEST_BASE_URL });

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toContain('limit=25');
      expect(url).toContain('cursor=abc123');
    });

    it('uses custom baseUrl when provided', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ agents: [], totalCount: 0, next: null }),
      });

      await fetchAgentRegistry({ baseUrl: 'http://localhost:8080' });

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toContain('http://localhost:8080/api/v1/registry/agents');
    });
  });

  describe('fetchAgentsBySkill (REST)', () => {
    let fetchSpy: ReturnType<typeof vi.fn>;
    const originalFetch = globalThis.fetch;

    beforeEach(() => {
      fetchSpy = vi.fn();
      globalThis.fetch = fetchSpy;
    });

    afterEach(() => {
      globalThis.fetch = originalFetch;
    });

    it('passes skill as query param', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ agents: [], totalCount: 0, next: null }),
      });

      await fetchAgentsBySkill('image-generation', { baseUrl: TEST_BASE_URL });

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toContain('skill=image-generation');
      expect(url).toContain('include=full');
    });
  });

  describe('fetchAgentsByListing (REST)', () => {
    let fetchSpy: ReturnType<typeof vi.fn>;
    const originalFetch = globalThis.fetch;

    beforeEach(() => {
      fetchSpy = vi.fn();
      globalThis.fetch = fetchSpy;
    });

    afterEach(() => {
      globalThis.fetch = originalFetch;
    });

    it('passes listing=public as query param', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ agents: [], totalCount: 0, next: null }),
      });

      await fetchAgentsByListing('public', { baseUrl: TEST_BASE_URL });

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toContain('listing=public');
      expect(url).toContain('include=full');
    });

    it('passes listing=private as query param', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ agents: [], totalCount: 0, next: null }),
      });

      await fetchAgentsByListing('private', { baseUrl: TEST_BASE_URL });

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toContain('listing=private');
    });

  });

  describe('removeAgent (REST)', () => {
    let fetchSpy: ReturnType<typeof vi.fn>;
    const originalFetch = globalThis.fetch;

    beforeEach(() => {
      fetchSpy = vi.fn();
      globalThis.fetch = fetchSpy;
    });

    afterEach(() => {
      globalThis.fetch = originalFetch;
    });

    it('sends DELETE request and returns true on success', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ agentName: 'acme-echo', status: 'deleted', ts: Date.now() }),
      });

      const result = await removeAgent('acme-echo', { baseUrl: TEST_BASE_URL });

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe(`${TEST_BASE_URL}/api/v1/registry/agents?agentName=acme-echo`);
      expect(init.method).toBe('DELETE');
      expect(result).toBe(true);
    });

    it('returns false on 404', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 404,
        json: async () => ({ code: 'NotFound', message: 'Agent not found' }),
      });

      const result = await removeAgent('nonexistent', { baseUrl: TEST_BASE_URL });
      expect(result).toBe(false);
    });
  });
});
