// Managed sessions against Connectors (live): History, Presence, Publish and Subscribe on a
// customer keyset. The Connectors and the keyset are provisioned beforehand; this file only
// consumes them, and runs once per keyset with that keyset's environment:
//
//   CONNECTOR_SUBSCRIBE_AGENT, CONNECTOR_HISTORY_AGENT  Connector agent names (required)
//   CONNECTOR_PRESENCE_AGENT, CONNECTOR_PUBLISH_AGENT   optional; their cases skip when unset
//   CONNECTOR_CHANNEL_PREFIX     the Connectors' Channel Template is `<prefix>.{show}`
//   CONNECTOR_SUBSCRIBE_KEY, CONNECTOR_PUBLISH_KEY      the keyset, for this test's own publisher
//   CONNECTOR_SECRET_KEY         required when CONNECTOR_KEYSET_MODE is `access_manager`
//   CONNECTOR_KEYSET_MODE        `access_manager` or `consent`
//
// Also needs the variables of session-harness.ts (PUBNUB_LIVE_TEST, BLOCKS_API_KEY,
// BLOCKS_BACKEND_URL, SESSIONS_FLAG_ON). Never prints or asserts on a key or token.
import { randomBytes } from 'node:crypto';
import PubNub from 'pubnub';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { getTestTimeout } from '../../helpers/live-test-config.js';
import {
  artifactText,
  createConsumer,
  liveGuard,
  sessionsFlagOn,
  sleep,
} from './session-harness.js';
import {
  type BlocksClient,
  type ExchangeOutcome,
  loadSessionSdk,
  type Message,
  type Session,
  type SessionSdk,
} from './session-sdk.js';

const env = (name: string): string => process.env[name] ?? '';

const subscribeAgent = env('CONNECTOR_SUBSCRIBE_AGENT');
const historyAgent = env('CONNECTOR_HISTORY_AGENT');
const presenceAgent = env('CONNECTOR_PRESENCE_AGENT');
const publishAgent = env('CONNECTOR_PUBLISH_AGENT');
const channelPrefix = env('CONNECTOR_CHANNEL_PREFIX');
const keysetMode = env('CONNECTOR_KEYSET_MODE');

const connectorEnvMissing =
  !subscribeAgent ||
  !historyAgent ||
  !channelPrefix ||
  !env('CONNECTOR_SUBSCRIBE_KEY') ||
  !env('CONNECTOR_PUBLISH_KEY') ||
  (keysetMode !== 'access_manager' && keysetMode !== 'consent') ||
  (keysetMode === 'access_manager' && !env('CONNECTOR_SECRET_KEY'));

const STREAM_WAIT_MS = 30_000;
const HISTORY_WAIT_MS = 30_000;
const POLL_MS = 1_000;
const MARKER_INTERVAL_MS = 500;
const CLOSE_WAIT_MS = 15_000;
const CASE_TIMEOUT_MS = 120_000;

const MESSAGE_COUNT = 3;

const runId = randomBytes(4).toString('hex');
let showCounter = 0;
// A show is one value of the Channel Template, so each case gets channels of its own.
const newShow = () => `show_${runId}_${showCounter++}`;
const channelOf = (show: string) => `${channelPrefix}.${show}`;

const dataPart = (data: Record<string, unknown>) => [{ type: 'data', data }];

describe.skipIf(liveGuard || !sessionsFlagOn || connectorEnvMissing)(
  `Connectors over managed sessions, ${keysetMode} keyset (live)`,
  () => {
    let sdk: SessionSdk;
    let client: BlocksClient;
    let publisher: PubNub;
    const publisherId = `connector-live-${runId}`;
    const sessions: Session[] = [];

    beforeAll(async () => {
      sdk = await loadSessionSdk();
      client = await createConsumer(sdk);
      publisher = new PubNub({
        subscribeKey: env('CONNECTOR_SUBSCRIBE_KEY'),
        publishKey: env('CONNECTOR_PUBLISH_KEY'),
        ...(env('CONNECTOR_SECRET_KEY') && { secretKey: env('CONNECTOR_SECRET_KEY') }),
        userId: publisherId,
      });
    });

    afterEach(async () => {
      for (const session of sessions.splice(0)) {
        await session.close().catch((err: unknown) => console.warn('[cleanup] close failed:', err));
      }
    });

    afterAll(() => {
      client?.destroy();
      publisher?.destroy();
    });

    async function open(agentName: string): Promise<Session> {
      const session = await client.openSession(agentName);
      sessions.push(session);
      return session;
    }

    async function publishRun(show: string, count: number): Promise<number[]> {
      const sequence = Array.from({ length: count }, (_, index) => index + 1);
      for (const seq of sequence) {
        await publisher.publish({
          channel: channelOf(show),
          message: { run: runId, seq },
          storeInHistory: true,
        });
      }
      return sequence;
    }

    /**
     * stream() does not wait for the stream client to connect, and the client replays no history,
     * so a message published before it is subscribed is lost. Markers go out until one arrives,
     * and only then does a case publish the messages it asserts on.
     */
    async function publishMarkersUntilReceived(show: string, received: Message[]) {
      const deadline = Date.now() + STREAM_WAIT_MS;
      while (received.length === 0) {
        if (Date.now() > deadline) throw new Error('the stream client did not connect in time');
        await publisher.publish({ channel: channelOf(show), message: { marker: runId } });
        await sleep(MARKER_INTERVAL_MS);
      }
    }

    /** Collects the stream in the background; `done` settles when iteration ends or throws. */
    function collect(stream: AsyncIterable<Message>) {
      const received: Message[] = [];
      const done = (async () => {
        for await (const message of stream) received.push(message);
      })();
      done.catch(() => undefined);
      const waitFor = (predicate: () => boolean) =>
        expect.poll(predicate, { timeout: STREAM_WAIT_MS, interval: 250 }).toBe(true);
      return { received, done, waitFor };
    }

    const ownMessages = (messages: Message[]) =>
      messages.filter((m) => (m.message as { run?: string } | null)?.run === runId);
    const seqOf = (message: Message) => (message.message as { seq: number }).seq;

    async function expectIterationEnds(done: Promise<void>) {
      const outcome = await Promise.race([
        done.then(() => 'ended' as const),
        sleep(CLOSE_WAIT_MS).then(() => 'timeout' as const),
      ]);
      expect(outcome).toBe('ended');
    }

    function expectLiveMessage(message: Message, show: string) {
      expect(message).toMatchObject({
        templateValue: show,
        channel: channelOf(show),
        publisher: publisherId,
        messageType: -1,
      });
      expect(message.timetoken).toMatch(/^\d+$/);
    }

    function inlineJson<T>(outcome: ExchangeOutcome): T {
      expect(outcome.artifacts).toHaveLength(1);
      return JSON.parse(artifactText(outcome.artifacts[0])) as T;
    }

    it(
      'History call() returns the published messages, newest first, in the Message shape',
      async () => {
        const show = newShow();
        await publishRun(show, MESSAGE_COUNT);

        // History is stored a moment after the publish returns.
        let outcome: ExchangeOutcome | undefined;
        let messages: Message[] = [];
        await expect
          .poll(
            async () => {
              outcome = await client.call(historyAgent, dataPart({ show, limit: 10 }));
              messages =
                outcome.state === 'completed'
                  ? inlineJson<{ messages: Message[] }>(outcome).messages
                  : [];
              return messages.length;
            },
            { timeout: HISTORY_WAIT_MS, interval: POLL_MS },
          )
          .toBe(MESSAGE_COUNT);

        expect(outcome?.state).toBe('completed');
        expect(messages.map((m) => (m.message as { seq: number }).seq)).toEqual([3, 2, 1]);
        for (const message of messages) expectLiveMessage(message, show);
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it.skipIf(!presenceAgent)(
      'Presence call() answers completed with an occupancy count',
      async () => {
        const outcome = await client.call(presenceAgent, dataPart({ show: newShow() }));

        expect(outcome.state).toBe('completed');
        expect(inlineJson<{ occupancy: number }>(outcome).occupancy).toBeGreaterThanOrEqual(0);
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it.skipIf(!publishAgent)(
      'Publish call() answers completed and the message reaches the channel',
      async () => {
        const show = newShow();
        const text = `connector-live-${runId}`;

        const outcome = await client.call(publishAgent, dataPart({ show, fields: { text } }));
        expect(outcome.state).toBe('completed');
        expect(inlineJson<{ timetoken: string }>(outcome).timetoken).toMatch(/^\d+$/);

        let texts: string[] = [];
        await expect
          .poll(
            async () => {
              const history = await client.call(historyAgent, dataPart({ show }));
              texts =
                history.state === 'completed'
                  ? inlineJson<{ messages: Message[] }>(history).messages.map(
                      (m) => (m.message as { text?: string }).text ?? '',
                    )
                  : [];
              return texts;
            },
            { timeout: HISTORY_WAIT_MS, interval: POLL_MS },
          )
          .toEqual([text]);
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it(
      `send() then stream() delivers 3 published messages in order, and close() ends iteration (${keysetMode})`,
      async () => {
        const show = newShow();
        const session = await open(subscribeAgent);

        const stream = collect(session.send(dataPart({ show })).stream());
        await publishMarkersUntilReceived(show, stream.received);
        await publishRun(show, MESSAGE_COUNT);
        await stream.waitFor(() => ownMessages(stream.received).length >= MESSAGE_COUNT);

        const own = ownMessages(stream.received);
        expect(own.map(seqOf)).toEqual([1, 2, 3]);
        for (const message of own) expectLiveMessage(message, show);

        await session.close();
        await expectIterationEnds(stream.done);
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it(
      'a Subscribe with no input follows every show and derives each templateValue',
      async () => {
        const [showA, showB] = [newShow(), newShow()];
        const session = await open(subscribeAgent);

        const stream = collect(session.send([]).stream());
        await publishMarkersUntilReceived(showA, stream.received);
        await publishRun(showA, 1);
        await publishRun(showB, 1);
        await stream.waitFor(() => ownMessages(stream.received).length >= 2);

        const own = ownMessages(stream.received);
        expect(own.map((m) => m.channel).sort()).toEqual(
          [channelOf(showA), channelOf(showB)].sort(),
        );
        for (const message of own) {
          expect(message.templateValue).toBe(message.channel.slice(`${channelPrefix}.`.length));
        }
        expect(own.map((m) => m.templateValue).sort()).toEqual([showA, showB].sort());
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it(
      'a second Subscribe exchange in the session fails with CONNECTOR_STREAM_ALREADY_OPEN and its stream() throws SessionStreamError',
      async () => {
        const session = await open(subscribeAgent);
        const first = session.send(dataPart({ show: newShow() }));
        await expect(first.waitForOutcome(STREAM_WAIT_MS)).resolves.toMatchObject({
          state: 'completed',
        });

        const second = session.send(dataPart({ show: newShow() }));
        await expect(second.waitForOutcome(STREAM_WAIT_MS)).resolves.toMatchObject({
          state: 'failed',
          reason: 'CONNECTOR_STREAM_ALREADY_OPEN',
        });
        const error = await collect(second.stream()).done.catch((e: unknown) => e);

        expect(error).toBeInstanceOf(sdk.SessionStreamError);
        expect(error).toMatchObject({ code: 'CONNECTOR_STREAM_ALREADY_OPEN', state: 'failed' });
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it(
      'call() on a Subscribe Connector settles failed with CONNECTOR_KIND_NOT_REQUEST',
      async () => {
        const outcome = await client.call(subscribeAgent, dataPart({ show: newShow() }));

        expect(outcome).toMatchObject({
          state: 'failed',
          reason: 'CONNECTOR_KIND_NOT_REQUEST',
        });
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );

    it(
      'send() on a History Connector completes and its stream() ends empty',
      async () => {
        const session = await open(historyAgent);
        const exchange = session.send(dataPart({ show: newShow() }));
        await expect(exchange.waitForOutcome(HISTORY_WAIT_MS)).resolves.toMatchObject({
          state: 'completed',
        });

        const stream = collect(exchange.stream());

        await expectIterationEnds(stream.done);
        expect(stream.received).toEqual([]);
      },
      getTestTimeout(CASE_TIMEOUT_MS),
    );
  },
);
