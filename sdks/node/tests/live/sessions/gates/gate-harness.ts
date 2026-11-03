import { appendFileSync } from 'node:fs';
import { join } from 'node:path';
import PubNub from 'pubnub';
import { fetchCdmConfig } from '../../../../src/runtime/cdm-config.js';
import {
  CURRENT_PROTOCOL_VERSION,
  PROTOCOL_VERSION_HEADER,
} from '../../../../src/runtime/protocol-version.js';
import { mapTransportCategory } from '../../../../src/runtime/transport-categories.js';
import type { BackendCall } from '../session-harness.js';

export type Keys = { subscribeKey: string; publishKey: string };

/** One minted session token and the identity and channel it was minted for. */
export interface Credential {
  pamToken: string;
  uuid: string;
  channel: string;
}

export type Verdict = 'granted' | 'denied';

const SUBSCRIBE_VERDICT_MS = 15_000;

/** The free-agent keyset, resolved as the SDK does: env overrides first, then the CDM. */
export async function sessionKeys(): Promise<Keys> {
  const { playground } = await fetchCdmConfig();
  return {
    subscribeKey: process.env.BLOCKS_SUBSCRIBE_KEY ?? playground.subscribeKey,
    publishKey: process.env.BLOCKS_PUBLISH_KEY ?? playground.publishKey,
  };
}

function clientFor(keys: Keys, credential: Credential): PubNub {
  // No leave on teardown: a probe shares its UUID with a live SDK client, whose counterpart must
  // not see it leave.
  const pubnub = new PubNub({ ...keys, userId: credential.uuid, suppressLeaveEvents: true });
  pubnub.setToken(credential.pamToken);
  return pubnub;
}

async function verdictOf(keys: Keys, credential: Credential, op: (pn: PubNub) => Promise<unknown>) {
  const pubnub = clientFor(keys, credential);
  try {
    await op(pubnub);
    return 'granted';
  } catch (err) {
    const status = (err as { status?: { statusCode?: number } }).status;
    if (status?.statusCode === 403) return 'denied';
    throw err;
  } finally {
    pubnub.destroy();
  }
}

/** Reads one message of history: PAM's read check, answered synchronously. */
export function readVerdict(keys: Keys, credential: Credential, channel = credential.channel) {
  return verdictOf(keys, credential, (pn) => pn.fetchMessages({ channels: [channel], count: 1 }));
}

export function publishVerdict(keys: Keys, credential: Credential, channel: string) {
  return verdictOf(keys, credential, (pn) => pn.publish({ channel, message: { gateProbe: true } }));
}

/** Subscribes until PubNub reports either a connection or a denial. */
export function subscribeVerdict(
  keys: Keys,
  credential: Credential,
  channel: string,
): Promise<Verdict> {
  const pubnub = clientFor(keys, credential);
  return new Promise<Verdict>((resolve, reject) => {
    const settle = (outcome: () => void) => {
      clearTimeout(timer);
      pubnub.unsubscribeAll();
      pubnub.destroy();
      outcome();
    };
    const timer = setTimeout(
      () => settle(() => reject(new Error(`subscribe to ${channel} neither connected nor was denied`))),
      SUBSCRIBE_VERDICT_MS,
    );
    pubnub.addListener({
      status: (status) => {
        const category = mapTransportCategory(status);
        if (category === 'connected') settle(() => resolve('granted'));
        if (category === 'access_denied') settle(() => resolve('denied'));
      },
    });
    pubnub.subscribe({ channels: [channel] });
  });
}

export const parseToken = (keys: Keys, pamToken: string) => {
  const pubnub = new PubNub({ ...keys, userId: 'gate-token-parser' });
  try {
    const parsed = pubnub.parseToken(pamToken);
    if (!parsed) throw new Error('PubNub could not parse a minted session token');
    return parsed;
  } finally {
    pubnub.destroy();
  }
};

export function answered(calls: BackendCall[], method: string, path: string | RegExp) {
  const matches = (p: string) => (typeof path === 'string' ? p === path : path.test(p));
  return calls.filter((c) => c.method === method && matches(c.path) && c.status < 300);
}

/** Replays a recorded SDK call with the same caller's credentials, as its own refresh would. */
export async function replay(baseUrl: string, call: BackendCall, path: string): Promise<unknown> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
      ...(call.authorization ? { Authorization: call.authorization } : {}),
    },
    body: '{}',
  });
  if (!response.ok) throw new Error(`POST ${path} failed: HTTP ${response.status}`);
  return response.json();
}

/**
 * Appends a line to this gate's report and the test log. Each gate writes its own file under
 * SESSION_GATES_REPORT_DIR, so gate files running in parallel cannot interleave their tables.
 */
export function report(gate: 'token-grants' | 'token-revocation', line: string): void {
  console.log(`[gate] ${line}`);
  const dir = process.env.SESSION_GATES_REPORT_DIR;
  if (dir) appendFileSync(join(dir, `${gate}.md`), `${line}\n`);
}
