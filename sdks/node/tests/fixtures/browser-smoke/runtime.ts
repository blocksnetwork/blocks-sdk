/**
 * Browser execution fixture for the Family B runtime test.
 *
 * This fixture is IMPORTED FROM `tests/browser-execution.test.ts` and
 * shares the test's hoisted `vi.mock('pubnub', ...)` binding -- the
 * module factory replaces the `pubnub` default export for the entire
 * test file, and any `import PubNub from 'pubnub'` inside the SDK
 * source resolves to the same mock instance that the test configured.
 *
 * The fixture intentionally uses the `new TaskClient({...})` direct
 * constructor rather than `TaskClient.create({...})`: `create()` fires
 * a CDM HTTP fetch and ConsumerAuth setup that Family B does not need
 * to exercise. The file-part processing path we want to gate lives in
 * `sendMessage()`, which only depends on the constructor surface.
 */

import {
  TaskClient,
  type SendMessageRequestPart,
} from '../../../src/runtime/task-client.js';
import { TaskSession } from '../../../src/runtime/task-session.js';
import type { ArtifactRef } from '../../../src/runtime/artifacts.js';

/**
 * Build a TaskClient wired against a mocked RPC backend. Caller is
 * responsible for installing the global `fetch` stub via vitest
 * `vi.spyOn(globalThis, 'fetch')` or equivalent before invoking
 * `sendFileParts`.
 */
export function createTaskClient(baseUrl: string): TaskClient {
  return new TaskClient({
    subscribeKey: 'sub-browser-test',
    publishKey: 'pub-browser-test',
    baseUrl,
    billingMode: 'free',
  });
}

/**
 * Build a TaskSession pre-closed (no PubNub subscription) for tests
 * that exercise `downloadArtifact` against a file-kind ArtifactRef.
 * Pre-closed sessions lazily construct a `new PubNub(...)` for the
 * download, which hits the hoisted `vi.mock('pubnub', ...)` in the
 * test file.
 */
export function createPreClosedTaskSession(opts?: {
  readToken?: string;
}): TaskSession {
  return new TaskSession({
    taskId: 'task-browser-test',
    ownerId: 'user-browser-test',
    orgId: 'org-browser-test',
    readToken: opts?.readToken ?? 'T4-read-token',
    statusChannel: 'u.org-browser-test.task-browser-test',
    agentName: 'mock-agent',
    pubnub: null,
    ownsSubscribeClient: false,
    sdkOptions: {
      subscribeKey: 'sub-browser-test',
      publishKey: 'pub-browser-test',
    },
    preClosed: true,
    state: 'completed',
  });
}

/**
 * Exercise `sendMessage` with three inline-sized file inputs:
 * `Uint8Array`, `Blob`, and `File`. Each should succeed after
 * Family A lands. Today, the `Blob` and `File` calls throw because
 * `task-client.ts:653` runs `Buffer.from(part.file)` on them.
 *
 * The caller is responsible for ensuring `globalThis.fetch` responds
 * with a valid SendMessage JSON-RPC result for each call.
 */
export async function sendFileParts(client: TaskClient): Promise<{
  u8Session: TaskSession;
  blobSession: TaskSession;
  fileSession: TaskSession;
}> {
  const asUint8 = new Uint8Array([1, 2, 3]);
  const asBlob = new Blob([new Uint8Array([4, 5, 6])], {
    type: 'application/octet-stream',
  });
  const asFile = new File([new Uint8Array([7, 8, 9])], 'test.bin', {
    type: 'application/octet-stream',
  });

  // Uint8Array passes today -- regression guard.
  const u8Session = await client.sendMessage({
    agentName: 'mock-agent',
    requestParts: [
      {
        partId: 'u8',
        file: asUint8,
        fileName: 'u8.bin',
        contentType: 'application/octet-stream',
      },
    ],
    ownerId: 'user-browser-test',
  });

  // Blob fails today (`Buffer.from(blob)` throws in Node -- TypeError
  // "The first argument must be of type string or an instance of
  // Buffer, ArrayBuffer, or Array or an Array-like Object").
  // The SDK's SendMessageRequestPart type does not list Blob, so the
  // cast below is intentional: Family A will widen the public type to
  // accept Blob and File.
  const blobSession = await client.sendMessage({
    agentName: 'mock-agent',
    requestParts: [
      {
        partId: 'blob',
        file: asBlob as unknown as SendMessageRequestPart['file'],
        fileName: 'blob.bin',
        contentType: 'application/octet-stream',
      } as SendMessageRequestPart,
    ],
    ownerId: 'user-browser-test',
  });

  // File fails today (same Buffer.from failure mode as Blob).
  const fileSession = await client.sendMessage({
    agentName: 'mock-agent',
    requestParts: [
      {
        partId: 'file',
        file: asFile as unknown as SendMessageRequestPart['file'],
        fileName: 'file.bin',
        contentType: 'application/octet-stream',
      } as SendMessageRequestPart,
    ],
    ownerId: 'user-browser-test',
  });

  return { u8Session, blobSession, fileSession };
}

/**
 * Build a canonical file-kind ArtifactRef for `downloadArtifact` tests.
 * The values are arbitrary but must satisfy the PAM-aware `channel`,
 * `fileId`, and `fileName` required-fields check in
 * `artifacts.ts:downloadArtifact()`.
 */
export function fileRef(): ArtifactRef {
  return {
    kind: 'file',
    mimeType: 'application/octet-stream',
    size: 3,
    fileId: 'file-browser-test',
    fileName: 'test.bin',
    channel: 'u.org-browser-test.task-browser-test',
  };
}
