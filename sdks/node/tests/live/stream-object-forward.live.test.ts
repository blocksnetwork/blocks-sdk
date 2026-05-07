/**
 * Live A2A test for BLOCKS-262 — handler-side `StreamObject` parity with
 * the consumer-side `StreamClient` read/error surface.
 *
 * Two scenarios:
 *
 * 1. Input bytes path. A pipe agent declares one inbound `format: bytes`
 *    stream (`audio_in`) and one outbound `format: events` stream
 *    (`text_out`). The handler iterates `audioIn.bytes()` and writes
 *    `{ chunkLen, firstByte }` events to `textOut`. The test consumer
 *    opens `audio_in` via `streamRef.open()` and calls `.write(uint8Array)`
 *    three times with distinct typed-array payloads — typed-array writes
 *    land on the wire as `encoding: "base64"`. Reading `text_out` via
 *    `events<...>()` proves the handler-side `bytes()` decode path
 *    produced correct lengths and leading bytes for all three chunks.
 *
 *    DO NOT pass a base64 string literal to `.write(...)` — it lands as
 *    `encoding: "utf8"` and exercises the wrong path.
 *
 * 2. onError path. The handler-side `StreamObject` wraps the agent-side
 *    `StreamClient` built with token T7a (agent-instance.ts:1417). Driving
 *    a T7a-side error (T7a revocation OR agent-side PAM-denied channel)
 *    is the only way to assert the handler-side forwarding works. T7c
 *    revocation targets the consumer-side `StreamClient` and would not
 *    test this code path. `session.cancel()` is cooperative and does NOT
 *    surface a `StreamError`.
 *
 *    `onError(cb)` MUST be registered before the read path activates —
 *    `StreamClient.onError()` only appends to its callback list, it does
 *    NOT replay past errors to late registrants.
 *
 * Gated behind `PUBNUB_LIVE_TEST=1` like the other live tests. Run with:
 *   PUBNUB_LIVE_TEST=1 npm test -- stream-object-forward.live
 */
import { afterAll, describe, expect, it } from 'vitest';
import PubNub from 'pubnub';
import { startAgentInstance } from '../../src/runtime/agent-instance.js';
import type { StreamObject } from '../../src/runtime/stream-context.js';
import type { StreamError } from '../../src/stream/index.js';
import { TaskClient } from '../../src/runtime/task-client.js';
import { removeAgent } from '../../src/runtime/agent-registry.js';
import { makePipeTestCard } from '../helpers/test-card.js';
import {
  hasLiveEnv,
  hasBackendEnv,
  getBaseUrl,
  getTestTimeout,
  publishAgent,
} from '../helpers/live-test-config.js';

const liveGuard =
  process.env.PUBNUB_LIVE_TEST !== '1' || !hasLiveEnv() || !hasBackendEnv();

describe.skipIf(liveGuard)('StreamObject forwarded surface (live)', () => {
  const createdAgentNames: string[] = [];

  afterAll(async () => {
    const baseUrl = getBaseUrl();
    for (const name of createdAgentNames) {
      try {
        await removeAgent(name, { baseUrl });
      } catch (err) {
        console.warn(`[cleanup] removeAgent(${name}) failed:`, err);
      }
    }
  });

  // SKIPPED: blocked on the same PAM-denied heartbeat issue as
  // Scenario 1 in tests/live_stream_redesign_e2e.test.ts. The agent's
  // control-channel subscription is denied by PubNub edge ~1s after
  // start; the test then times out waiting on the consumer-driven
  // stream because the handler never runs. Re-enable once the E2E
  // backend's PUBNUB_PLAYGROUND_* keyset is reconciled with the GH
  // `secrets.PUBNUB_PLAYGROUND_*` values used by CI.
  // Tracking: TODO add issue/PR link before merge.
  // When un-skipping: replace the hardcoded `setTimeout(3000)` "settle"
  // below with a deterministic await on PNConnectedCategory before the
  // first SendMessage — the sleep masks a real connect-race condition.
  it.skip('handler-side bytes() yields correctly-decoded Uint8Array chunks end-to-end', async () => {
    const agentName = `bytes_forward_${Date.now()}`;
    const instanceUuid = `AG-${agentName}-${Date.now()}`;
    createdAgentNames.push(agentName);

    // Three distinct typed-array payloads. firstByte values are unique so
    // the consumer can verify ordering as well as length.
    const payloads: Uint8Array[] = [
      new Uint8Array([0x10, 0x20, 0x30]),
      new Uint8Array([0x40, 0x41, 0x42, 0x43]),
      new Uint8Array([0x77, 0x78, 0x79, 0x7a, 0x7b]),
    ];

    const card = makePipeTestCard({
      identity: {
        agentName,
        displayName: agentName,
        description: 'BLOCKS-262 forwarded-surface live test agent',
        version: '1.0.0',
        provider: { organization: 'blocks-net' },
      },
      streams: {
        audio_in: { direction: 'inbound', format: 'bytes' },
        text_out: { direction: 'outbound', format: 'events' },
      },
    });

    const handler = async (
      _task: unknown,
      ctx: {
        createStream: (opts?: { declaredStream?: string }) => Promise<StreamObject>;
      },
    ) => {
      const audioIn = await ctx.createStream({ declaredStream: 'audio_in' });
      const textOut = await ctx.createStream({ declaredStream: 'text_out' });

      // onError MUST register before the read path activates.
      audioIn.onError((err: StreamError) => {
        console.warn(`[handler] audioIn onError: category=${err.category} fatal=${err.fatal}`);
      });

      for await (const chunk of audioIn.bytes()) {
        textOut.write({ chunkLen: chunk.byteLength, firstByte: chunk[0] });
      }
      await textOut.end();
      return { artifacts: [{ data: 'done', mimeType: 'text/plain' }] };
    };

    // Publish before connecting — connectAgent does not upsert (post-PR-313).
    await publishAgent(agentName, card);

    const agent = await startAgentInstance({
      pubnub: new PubNub({
        publishKey: process.env.PUBNUB_PUBLISH_KEY!,
        subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
        userId: instanceUuid,
        secretKey: process.env.PUBNUB_SECRET_KEY!,
        enableEventEngine: true,
      }),
      agentName,
      card,
      instanceId: instanceUuid,
      baseUrl: getBaseUrl(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      handler: handler as any,
    });

    try {
      // controlChannel is populated asynchronously after connectAgent
      // resolves (agent-instance.ts:2010-2025). Wait for it — and a brief
      // beat for PubNub PNConnectedCategory — so the agent is subscribed
      // before the consumer fires SendMessage.
      for (let i = 0; i < 100 && !agent.controlChannel; i++) {
        await new Promise((r) => setTimeout(r, 100));
      }
      expect(agent.controlChannel).toBeTruthy();
      await new Promise((r) => setTimeout(r, 3000));

      const apiKey = process.env.BLOCKS_API_KEY;
      if (!apiKey) throw new Error('BLOCKS_API_KEY required for live test');

      const consumer = await TaskClient.create({
        billingMode: 'free',
        apiKey,
        baseUrl: getBaseUrl(),
      });

      const session = await consumer.sendMessage({
        agentName,
        taskKind: 'pipe',
        duration: 1,
        requestParts: [{ partId: 'noop', text: 'noop' }],
      });

      // Open audio_in (consumer writes typed-array payloads) and text_out
      // (consumer reads decoded events emitted by the handler).
      const audioInRef = await session.waitForStream('audio_in');
      const textOutRef = await session.waitForStream('text_out');
      const audioIn = audioInRef.open();
      const textOut = textOutRef.open();

      // Write three typed-array payloads — these go on the wire as base64.
      for (const p of payloads) {
        audioIn.write(p);
      }
      await audioIn.end();

      const observed: Array<{ chunkLen: number; firstByte: number }> = [];
      for await (const ev of textOut.events<{ chunkLen: number; firstByte: number }>()) {
        observed.push(ev);
        if (observed.length >= payloads.length) break;
      }

      expect(observed).toHaveLength(payloads.length);
      for (let i = 0; i < payloads.length; i++) {
        expect(observed[i].chunkLen).toBe(payloads[i].byteLength);
        expect(observed[i].firstByte).toBe(payloads[i][0]);
      }

      session.close();
      consumer.destroy();
    } finally {
      agent.stop();
    }
  }, getTestTimeout(60_000));

  // SKIPPED: this test is incomplete and will hang with PUBNUB_LIVE_TEST=1.
  // It awaits `errorSeen` (resolved only from the handler's
  // audioIn.onError(...)) but never starts a task, opens/writes the
  // stream, revokes T7a, or otherwise creates a PAM-denied condition.
  // The handler only runs once a task creates the stream, so the
  // promise never resolves and the test sits until the vitest timeout.
  // It also doesn't retain the returned agent handle, so teardown
  // can't run.
  //
  // Resolution path: wire a deterministic T7a-revocation or agent-side
  // PAM-denied-channel trigger (mirroring the bytes test's task-lifecycle
  // setup), then re-enable. See BLOCKS-262 acceptance criteria for the
  // live A2A error path.
  it.skip('handler-side onError fires with a StreamError on T7a-path PAM denial', async () => {
    // The handler-side StreamObject wraps a StreamClient built with T7a
    // (agent-instance.ts:1417, SDK_CONTRACT §8.2.6). To exercise the
    // forwarded onError, the test must drive a T7a-path error — either
    // T7a revocation mid-stream OR an agent-side PAM-denied channel.
    // T7c revocation only triggers the *consumer*-side StreamClient.onError.
    // session.cancel() is cooperative and does not surface a StreamError.
    const agentName = `onerror_forward_${Date.now()}`;
    const instanceUuid = `AG-${agentName}-${Date.now()}`;
    createdAgentNames.push(agentName);

    const errorSeen = new Promise<StreamError>((resolve) => {
      const card = makePipeTestCard({
        identity: {
          agentName,
          displayName: agentName,
          description: 'BLOCKS-262 onError forwarding live test',
          version: '1.0.0',
          provider: { organization: 'blocks-net' },
        },
        streams: {
          audio_in: { direction: 'inbound', format: 'bytes' },
        },
      });

      const handler = async (
        _task: unknown,
        ctx: {
          createStream: (opts?: { declaredStream?: string }) => Promise<StreamObject>;
        },
      ) => {
        const audioIn = await ctx.createStream({ declaredStream: 'audio_in' });
        // CRITICAL: register before the read path activates.
        audioIn.onError((err: StreamError) => resolve(err));
        try {
          for await (const _chunk of audioIn.bytes()) {
            // drain
          }
        } catch {
          // PAM denial may also throw — onError still fires first.
        }
        return { artifacts: [{ data: 'done', mimeType: 'text/plain' }] };
      };

      // Start the agent. The PAM denial is driven by the test infrastructure:
      // either revoke the agent's T7a stream token mid-stream OR start the
      // agent against a stream channel its T7a grant cannot read.
      void startAgentInstance({
        pubnub: new PubNub({
          publishKey: process.env.PUBNUB_PUBLISH_KEY!,
          subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
          userId: instanceUuid,
          secretKey: process.env.PUBNUB_SECRET_KEY!,
          enableEventEngine: true,
        }),
        agentName,
        card,
        instanceId: instanceUuid,
        baseUrl: getBaseUrl(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        handler: handler as any,
      });
    });

    const err = await errorSeen;
    expect(err).toBeDefined();
    expect(err.category).toBe('PNAccessDeniedCategory');
    expect(err.fatal).toBe(true);
  }, getTestTimeout(60_000));
});
