import { vi } from 'vitest';
import { CURRENT_PROTOCOL_VERSION } from '../../src/runtime/protocol-version.js';

export const ROUTE_ID = 'route1';
export const ROUTE_CHANNEL = `sess.${ROUTE_ID}.*`;
export const PROVIDER_UUID = 'uuid-provider';
export const CONSUMER_UUID = 'uuid-consumer';
export const BACKEND_UUID = 'uuid-backend';

export const sessionChannel = (sessionId: string) => `sess.${ROUTE_ID}.${sessionId}`;

export function routeGrant(routeId = ROUTE_ID) {
  return {
    routeId,
    routeChannel: `sess.${routeId}.*`,
    pamToken: `token-${routeId}`,
    ttlMinutes: 720,
    providerTransportUuid: PROVIDER_UUID,
    backendTransportUuid: BACKEND_UUID,
  };
}

export function manifest(sessionId: string, routeId = ROUTE_ID) {
  return {
    sessionId,
    routeId,
    consumerTransportUuid: CONSUMER_UUID,
    agentName: 'echo_agent',
    orgId: 'org-1',
    ownerId: 'user-1',
    callerClaims: { sub: 'user-1' },
  };
}

export interface Published {
  channel: string;
  message: Record<string, unknown>;
  meta: Record<string, unknown>;
  storeInHistory: boolean;
}

/** `autoConnect: false` leaves the route subscribed but unconnected until `connect()`. */
export function fakeRouteClient(userId: string, { autoConnect = true } = {}) {
  const listeners: Array<{
    message?: (event: unknown) => void;
    status?: (event: unknown) => void;
  }> = [];
  const status = (category: string) => {
    for (const l of listeners) l.status?.({ category });
  };
  const connect = () => status('PNConnectedCategory');
  const published: Published[] = [];
  const client = {
    userId,
    published,
    addListener: vi.fn(
      (l: { message?: (event: unknown) => void; status?: (event: unknown) => void }) =>
        listeners.push(l),
    ),
    removeListener: vi.fn(),
    subscribe: vi.fn(() => {
      if (autoConnect) queueMicrotask(connect);
    }),
    connect,
    status,
    reconnect: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
    destroy: vi.fn(),
    publish: vi.fn(async (params: Published) => {
      published.push(params);
      return { timetoken: '1' };
    }),
    deliver(channel: string, publisher: string, message: unknown) {
      for (const l of listeners) l.message?.({ channel, publisher, message });
    },
    ofType(type: string) {
      return published.map((p) => p.message).filter((m) => m.type === type);
    },
  };
  return client;
}

export type FakeRouteClient = ReturnType<typeof fakeRouteClient>;

export function fromConsumer(
  sessionId: string,
  seq: number,
  type: string,
  fields: Record<string, unknown> = {},
) {
  return { type, protocolVersion: CURRENT_PROTOCOL_VERSION, sessionId, seq, ts: 0, ...fields };
}

export const textParts = (text: string) => [{ text }];
