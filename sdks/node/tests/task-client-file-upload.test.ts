/**
 * TaskClient.sendMessage file upload tests.
 *
 * Verifies that:
 * - Small files (<= 16 KB) are inlined as artifactRef on the request part
 * - Large files (> 16 KB) trigger the pre-signed URL upload flow
 * - uploadSessionId is included in the RPC params for uploaded files
 * - Non-file parts pass through unchanged
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { TaskClient } from '../src/runtime/task-client.js';

// Mock PubNub
vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => ({
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
    destroy: vi.fn(),
    time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
    fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
      channels: { [channels[0]]: [] },
    })),
  })),
}));

describe('TaskClient.sendMessage with files', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it('inlines small files as artifactRef on request parts', async () => {
    // The only fetch call should be the RPC call for SendMessage
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-inline-1',
          orgId: 'org1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    const smallFile = Buffer.from('Hello, world!'); // 13 bytes, well under 16 KB
    await client.sendMessage({
      agentName: 'test_agent',
      ownerId: 'user1',
      requestParts: [
        {
          partId: 'greeting',
          file: smallFile,
          fileName: 'hello.txt',
          contentType: 'text/plain',
        },
      ],
    });

    // Only one fetch call (SendMessage RPC), no upload calls
    expect(fetchSpy).toHaveBeenCalledTimes(1);

    // Verify the RPC payload includes an inline artifactRef
    const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
    expect(rpcBody.method).toBe('SendMessage');
    const parts = rpcBody.params.requestParts;
    expect(parts).toHaveLength(1);
    expect(parts[0].artifactRef).toBeDefined();
    expect(parts[0].artifactRef.kind).toBe('inline');
    expect(parts[0].artifactRef.data).toBe(smallFile.toString('base64'));
    expect(parts[0].artifactRef.fileName).toBe('hello.txt');
    // file and fileName should not be on the wire part
    expect(parts[0].file).toBeUndefined();
    expect(parts[0].fileName).toBeUndefined();
    // No uploadSessionId since all files are inline
    expect(rpcBody.params.uploadSessionId).toBeUndefined();
  });

  it('uploads large files and includes uploadSessionId', async () => {
    // Call 1: request-upload
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        uploadSessionId: 'session-abc',
        uploadId: 'upload-123',
        uploadUrl: 'https://s3.example.com/presigned',
        formFields: [{ key: 'Policy', value: 'pol' }],
      }),
    });
    // Call 2: S3 direct upload
    fetchSpy.mockResolvedValueOnce({ ok: true, status: 204 });
    // Call 3: confirm-upload
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ uploadId: 'upload-123' }),
    });
    // Call 4: SendMessage RPC
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-upload-1',
          orgId: 'org1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    const largeFile = Buffer.alloc(20000); // 20 KB, above 16 KB threshold
    await client.sendMessage({
      agentName: 'test_agent',
      ownerId: 'user1',
      requestParts: [
        {
          partId: 'document',
          file: largeFile,
          fileName: 'big.pdf',
          contentType: 'application/pdf',
        },
      ],
    });

    // 4 fetch calls total: request-upload, S3, confirm-upload, SendMessage
    expect(fetchSpy).toHaveBeenCalledTimes(4);

    // Verify the RPC SendMessage payload
    const rpcBody = JSON.parse(fetchSpy.mock.calls[3][1].body);
    expect(rpcBody.method).toBe('SendMessage');
    expect(rpcBody.params.uploadSessionId).toBe('session-abc');

    // Uploaded part carries partId + contentType. `artifactRef` and
    // raw `file` bytes are dropped — the backend reconstructs
    // artifactRef from the task_file row. `contentType` IS preserved
    // so agent handlers that branch on `part.contentType` continue to
    // work for files above the inline threshold (regression guard for
    // the PR #541 review — and for cross-SDK parity, since the Python
    // SDK has always preserved contentType on this path).
    const parts = rpcBody.params.requestParts;
    expect(parts).toHaveLength(1);
    expect(parts[0].partId).toBe('document');
    expect(parts[0].contentType).toBe('application/pdf');
    expect(parts[0].artifactRef).toBeUndefined();
    expect(parts[0].file).toBeUndefined();
    expect(parts[0].fileName).toBeUndefined();
  });

  it('does not read a large Blob into memory on the upload path', async () => {
    // The upload path must hand the Blob straight to FormData/fetch
    // without materializing its bytes. A naive `await input.arrayBuffer()`
    // in normalizeFileInput would allocate a full-size copy even for
    // multi-GB Blobs. This test guards against that regression by
    // spying on arrayBuffer() for a 64 KB Blob (comfortably above the
    // 16 KB inline threshold) and asserting zero calls.
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        uploadSessionId: 'session-no-eager-read',
        uploadId: 'upload-no-eager',
        uploadUrl: 'https://s3.example.com/presigned',
        formFields: [],
      }),
    });
    fetchSpy.mockResolvedValueOnce({ ok: true, status: 204 });
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ uploadId: 'upload-no-eager' }),
    });
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-no-eager',
          orgId: 'org1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    const largeBlob = new Blob([new Uint8Array(64 * 1024)]);
    const arrayBufferSpy = vi.fn(async (): Promise<ArrayBuffer> => {
      throw new Error(
        'Blob.arrayBuffer() must not be called on the upload path — ' +
          'normalizeFileInput should expose a lazy getBytes() that only ' +
          'runs on the inline branch.',
      );
    });
    Object.defineProperty(largeBlob, 'arrayBuffer', {
      value: arrayBufferSpy,
      writable: true,
      configurable: true,
    });

    await client.sendMessage({
      agentName: 'test_agent',
      ownerId: 'user1',
      requestParts: [
        {
          partId: 'document',
          file: largeBlob,
          fileName: 'big.bin',
          contentType: 'application/octet-stream',
        },
      ],
    });

    expect(arrayBufferSpy).not.toHaveBeenCalled();
  });

  it('handles mixed parts (text, inline file, uploaded file)', async () => {
    // Call 1: request-upload for large file
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        uploadSessionId: 'session-mix',
        uploadId: 'upload-mix',
        uploadUrl: 'https://s3.example.com/presigned',
        formFields: [],
      }),
    });
    // Call 2: S3 upload
    fetchSpy.mockResolvedValueOnce({ ok: true, status: 204 });
    // Call 3: confirm-upload
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ uploadId: 'upload-mix' }),
    });
    // Call 4: SendMessage RPC
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-mix-1',
          orgId: 'org1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    await client.sendMessage({
      agentName: 'test_agent',
      ownerId: 'user1',
      requestParts: [
        { partId: 'prompt', text: 'Summarize these files' },
        {
          partId: 'thumbnail',
          file: Buffer.from('tiny-image'),
          fileName: 'thumb.png',
          contentType: 'image/png',
        },
        {
          partId: 'dataset',
          file: Buffer.alloc(20000),
          fileName: 'data.csv',
          contentType: 'text/csv',
        },
      ],
    });

    const rpcBody = JSON.parse(fetchSpy.mock.calls[3][1].body);
    const parts = rpcBody.params.requestParts;
    expect(parts).toHaveLength(3);

    // Text part: unchanged
    expect(parts[0].partId).toBe('prompt');
    expect(parts[0].text).toBe('Summarize these files');
    expect(parts[0].artifactRef).toBeUndefined();

    // Small file: inlined
    expect(parts[1].partId).toBe('thumbnail');
    expect(parts[1].artifactRef).toBeDefined();
    expect(parts[1].artifactRef.kind).toBe('inline');

    // Large file: uploaded (only partId)
    expect(parts[2].partId).toBe('dataset');
    expect(parts[2].artifactRef).toBeUndefined();

    // uploadSessionId present
    expect(rpcBody.params.uploadSessionId).toBe('session-mix');
  });

  it('strips text from wire part when part has both text and file', async () => {
    // SendMessage RPC response
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-text-file-1',
          orgId: 'org1',
          extensions: { blocks: { readToken: null } },
        },
      }),
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    const smallFile = Buffer.from('file-content');
    await client.sendMessage({
      agentName: 'test_agent',
      ownerId: 'user1',
      requestParts: [
        {
          partId: 'doc',
          text: 'hello',
          file: smallFile,
          fileName: 'doc.txt',
          contentType: 'text/plain',
        },
      ],
    });

    // Verify the RPC payload
    const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
    const parts = rpcBody.params.requestParts;
    expect(parts).toHaveLength(1);
    // artifactRef should be present (inline)
    expect(parts[0].artifactRef).toBeDefined();
    expect(parts[0].artifactRef.kind).toBe('inline');
    // text must NOT be on the wire part
    expect(parts[0].text).toBeUndefined();
    // file and fileName should also be stripped
    expect(parts[0].file).toBeUndefined();
    expect(parts[0].fileName).toBeUndefined();
  });

  it('throws when file-bearing part has no partId', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    const smallFile = Buffer.from('some data');
    await expect(
      client.sendMessage({
        agentName: 'test_agent',
        ownerId: 'user1',
        requestParts: [
          { file: smallFile, fileName: 'data.bin' },
        ],
      }),
    ).rejects.toThrow('partId is required for file-bearing request parts');
  });

  it('throws when large file-bearing part has no partId', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      baseUrl: 'http://localhost:3001',
    });

    const largeFile = Buffer.alloc(20000);
    await expect(
      client.sendMessage({
        agentName: 'test_agent',
        ownerId: 'user1',
        requestParts: [
          { file: largeFile, fileName: 'big.pdf' },
        ],
      }),
    ).rejects.toThrow('partId is required for file-bearing request parts');
  });

  it('throws when large file but no baseUrl configured', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      // No baseUrl
    });

    const largeFile = Buffer.alloc(20000);
    await expect(
      client.sendMessage({
        agentName: 'test_agent',
        ownerId: 'user1',
        requestParts: [
          { partId: 'doc', file: largeFile, fileName: 'big.pdf' },
        ],
      }),
    ).rejects.toThrow('File upload requires a backend baseUrl');
  });
});
