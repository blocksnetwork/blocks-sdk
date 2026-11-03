// Python mirror: tests/test_session_fixtures_parity.py. Both load every case, no skip list.
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { CURRENT_PROTOCOL_VERSION } from '../src/runtime/protocol-version.js';
import { ConsumerSessionReceiver } from '../src/runtime/sessions/consumer-receiver.js';
import { ProviderSessionReceiver } from '../src/runtime/sessions/provider-receiver.js';
import type {
  ExchangeOutcome,
  ExchangeProgress,
  SessionParties,
  SessionState,
} from '../src/runtime/sessions/types.js';

const FIXTURES_DIR = fileURLToPath(new URL('../../../fixtures/sessions/', import.meta.url));

type Receiver = 'consumer' | 'provider';
type Party = 'consumer' | 'provider' | 'backend' | 'stranger';
type Message = Record<string, unknown> & { type: string };

interface TypeRule {
  required: string[];
  optional: string[];
  enums?: Record<string, string[]>;
  envelope?: string[];
}

interface Envelope {
  protocolVersion: string;
  sessionId: string;
  parties: Record<Party, string>;
  envelopeFields: string[];
  maxExchangeIdLength: number;
  types: Record<string, TypeRule>;
  accepts: Record<Receiver, Partial<Record<Party, '*' | string[]>>>;
  givenStates: Record<Receiver, string[]>;
  sessionStates: string[];
  expectKeys: Record<Receiver, string[]>;
}

type Step = { advanceMs: number } | { from: Party; message: Message; expect?: 'dispatch' | 'drop' };

interface FixtureCase {
  id: string;
  summary: string;
  receiver: Receiver;
  given: { state: string; pendingExchanges?: string[] };
  steps: Step[];
  expect: Record<string, unknown>;
}

function readJson<T>(file: string): T {
  return JSON.parse(readFileSync(path.join(FIXTURES_DIR, file), 'utf8')) as T;
}

const envelope = readJson<Envelope>('envelope.json');
const cases = readdirSync(path.join(FIXTURES_DIR, 'cases'))
  .filter((f) => f.endsWith('.json'))
  .sort()
  .flatMap((f) => readJson<{ cases: FixtureCase[] }>(path.join('cases', f)).cases);

function isDelivery(step: Step): step is Exclude<Step, { advanceMs: number }> {
  return 'from' in step;
}

function counterpartAccepts(receiver: Receiver, from: Party, type: string): boolean {
  const allowed = envelope.accepts[receiver][from];
  return allowed === '*' || (allowed ?? []).includes(type);
}

function fieldProblems(rule: TypeRule, message: Message): string[] {
  const envelopeFields = rule.envelope ?? envelope.envelopeFields;
  const allowed = new Set([...envelopeFields, ...rule.required, ...rule.optional]);
  return [
    ...[...envelopeFields, ...rule.required]
      .filter((field) => !(field in message))
      .map((field) => `missing ${field}`),
    ...Object.keys(message)
      .filter((field) => !allowed.has(field))
      .map((field) => `unknown field ${field}`),
  ];
}

function identityProblems(message: Message): string[] {
  const problems: string[] = [];
  if (message.protocolVersion !== envelope.protocolVersion) problems.push('protocolVersion');
  if ('sessionId' in message && message.sessionId !== envelope.sessionId)
    problems.push('sessionId');
  return problems;
}

function seqProblems(from: Party, message: Message): string[] {
  if (!('seq' in message)) return [];
  const { seq } = message;
  const valid =
    Number.isInteger(seq) && (from === 'backend' ? seq === 0 : (seq as number) >= 1);
  return valid ? [] : [`seq ${String(seq)} from ${from}`];
}

function exchangeIdProblems(message: Message): string[] {
  const ids = [message.exchangeId, (message.exchange as Message | undefined)?.exchangeId];
  return ids
    .filter((id) => id !== undefined)
    .filter(
      (id) => typeof id !== 'string' || id.length === 0 || id.length > envelope.maxExchangeIdLength,
    )
    .map((id) => `exchangeId ${String(id)}`);
}

function enumProblems(rule: TypeRule, message: Message): string[] {
  return Object.entries(rule.enums ?? {})
    .filter(([field, values]) => field in message && !values.includes(message[field] as string))
    .map(([field]) => field);
}

function messageProblems(from: Party, message: Message): string[] {
  const rule = envelope.types[message.type];
  if (!rule) return [`unknown type ${message.type}`];
  return [
    ...fieldProblems(rule, message),
    ...identityProblems(message),
    ...seqProblems(from, message),
    ...exchangeIdProblems(message),
    ...enumProblems(rule, message),
  ];
}

function headerProblems(c: FixtureCase): string[] {
  const problems: string[] = [];
  if (!c.summary) problems.push('summary');
  if (!envelope.givenStates[c.receiver]?.includes(c.given.state)) problems.push('given.state');
  const expectKeys = Object.keys(c.expect);
  if (expectKeys.length === 0) problems.push('empty expect');
  problems.push(
    ...expectKeys
      .filter((key) => !envelope.expectKeys[c.receiver].includes(key))
      .map((key) => `expect.${key}`),
  );
  const states = [c.expect.state, ...((c.expect.stateChanges as string[] | undefined) ?? [])];
  problems.push(
    ...states
      .filter((state) => state !== undefined && !envelope.sessionStates.includes(state as string))
      .map((state) => `state ${String(state)}`),
  );
  if (c.steps.length === 0) problems.push('no steps');
  return problems;
}

function deliveryProblems(
  receiver: Receiver,
  step: Exclude<Step, { advanceMs: number }>,
  processed: Set<string>,
): string[] {
  const problems: string[] = [];
  if (!(step.from in envelope.parties)) problems.push('from');
  problems.push(...messageProblems(step.from, step.message));
  if (!counterpartAccepts(receiver, step.from, step.message.type)) {
    if (step.expect !== 'drop') problems.push('non-counterpart must drop');
    return problems;
  }
  const key = `${step.from}:${String(step.message.seq)}`;
  // Backend notices all carry seq 0 and are exempt from dedup.
  const duplicate = 'seq' in step.message && step.from !== 'backend' && processed.has(key);
  processed.add(key);
  if (step.expect === 'drop' && !duplicate) problems.push('drop without cause');
  if (step.expect === 'dispatch' && duplicate) problems.push('duplicate dispatched');
  return problems;
}

function caseProblems(c: FixtureCase): string[] {
  const processed = new Set<string>();
  const stepProblems = c.steps.flatMap((step, i) => {
    if (isDelivery(step)) {
      return deliveryProblems(c.receiver, step, processed).map((p) => `step ${i}: ${p}`);
    }
    return Number.isInteger(step.advanceMs) && step.advanceMs > 0 ? [] : [`step ${i}`];
  });
  return [...headerProblems(c), ...stepProblems];
}

describe('session fixtures', () => {
  it('declare unique case ids', () => {
    const ids = cases.map((c) => c.id);
    expect(ids.length).toBeGreaterThan(0);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('use the protocol version the SDK publishes', () => {
    expect(envelope.protocolVersion).toBe(CURRENT_PROTOCOL_VERSION);
  });

  it.each(cases.map((c) => [c.id, c] as const))('%s is well-formed', (_id, c) => {
    expect(caseProblems(c)).toEqual([]);
  });
});

interface ReceiverHarness {
  deliver(publisher: string, message: Message): Promise<boolean>;
  advance(ms: number): Promise<void>;
  observed(): Record<string, unknown>;
}

// `deliver` resolves true when the message passes counterpart filtering and dedup.
// `recover()` finds no history, so a gap that triggers it is skipped.
type Context = Pick<Envelope, 'sessionId' | 'parties'>;

function partiesFor(receiver: Receiver, { sessionId, parties }: Context): SessionParties {
  const counterpart = receiver === 'consumer' ? 'provider' : 'consumer';
  return { sessionId, counterpartUuid: parties[counterpart], backendUuid: parties.backend };
}

function consumerHarness(given: FixtureCase['given'], context: Context) {
  const stateChanges: string[] = [];
  const outcomes: ExchangeOutcome[] = [];
  const progress: ExchangeProgress[] = [];
  let recoverCalls = 0;
  const session = new ConsumerSessionReceiver({
    parties: partiesFor('consumer', context),
    state: given.state as SessionState,
    recover: async () => {
      recoverCalls += 1;
    },
    events: {
      onStateChange: (state) => stateChanges.push(state),
      onOutcome: (outcome) => outcomes.push(outcome),
      onProgress: (event) => progress.push(event),
    },
  });
  for (const exchangeId of given.pendingExchanges ?? []) session.trackExchange(exchangeId);
  return {
    receive: (publisher: string, message: Message) => session.receive(publisher, message),
    observed: () => ({ state: session.state, stateChanges, outcomes, progress, recoverCalls }),
  };
}

function providerHarness(given: FixtureCase['given'], context: Context) {
  const exchangesStarted: string[] = [];
  const probeEchoes: string[] = [];
  let gapDetected = false;
  const session: ProviderSessionReceiver = new ProviderSessionReceiver({
    parties: partiesFor('provider', context),
    state: given.state as SessionState,
    events: {
      onExchange: (exchange) => exchangesStarted.push(exchange.exchangeId),
      onRouteProbe: (nonce) => probeEchoes.push(nonce),
      // No exchange is in flight, so the provider publishes session_closed at once.
      onCloseRequested: () => session.terminate('closed'),
      onGap: () => {
        gapDetected = true;
      },
    },
  });
  return {
    receive: (publisher: string, message: Message) => session.receive(publisher, message),
    observed: () => ({ state: session.state, exchangesStarted, probeEchoes, gapDetected }),
  };
}

async function createReceiverHarness(
  receiver: Receiver,
  given: FixtureCase['given'],
  context: Context,
): Promise<ReceiverHarness> {
  const session =
    receiver === 'consumer' ? consumerHarness(given, context) : providerHarness(given, context);
  return {
    deliver: async (publisher, message) => session.receive(publisher, message),
    advance: (ms) => vi.advanceTimersByTimeAsync(ms).then(() => undefined),
    observed: session.observed,
  };
}

describe('SDK against session fixtures', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  for (const c of cases) {
    it(c.id, async () => {
      const harness = await createReceiverHarness(c.receiver, c.given, envelope);
      for (const step of c.steps) {
        if (!isDelivery(step)) {
          await harness.advance(step.advanceMs);
          continue;
        }
        const dispatched = await harness.deliver(envelope.parties[step.from], step.message);
        if (step.expect) expect(dispatched).toBe(step.expect === 'dispatch');
      }
      const observed = harness.observed();
      for (const [key, value] of Object.entries(c.expect)) {
        expect(observed[key], key).toEqual(value);
      }
    });
  }
});
