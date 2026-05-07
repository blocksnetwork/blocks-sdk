import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  requestUpload,
  uploadToStorage,
  confirmUpload,
  uploadFile,
  type FileUploadAuth,
} from '../src/runtime/file-upload.js';
import { StaticAuthProvider } from '../src/runtime/auth-provider.js';

describe('file-upload', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  const auth: FileUploadAuth = {
    baseUrl: 'http://localhost:3001',
    authProvider: new StaticAuthProvider('test-jwt'),
  };

  describe('requestUpload', () => {
    it('calls the request-upload endpoint for consumer input', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          uploadSessionId: 'session-1',
          uploadId: 'upload-1',
          uploadUrl: 'https://s3.example.com/upload',
          formFields: [{ key: 'Policy', value: 'abc' }],
        }),
      });

      const result = await requestUpload(auth, {
        role: 'consumer-input',
        agentName: 'test_agent',
        fileName: 'report.pdf',
        fileSize: 50000,
        mimeType: 'application/pdf',
        partId: 'document',
      });

      expect(result.uploadSessionId).toBe('session-1');
      expect(result.uploadId).toBe('upload-1');
      expect(result.uploadUrl).toBe('https://s3.example.com/upload');
      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe('http://localhost:3001/api/v1/files/request-upload');
      expect(init.method).toBe('POST');
      const body = JSON.parse(init.body);
      expect(body.role).toBe('consumer-input');
      expect(body.agentName).toBe('test_agent');
    });

    it('calls the request-upload endpoint for provider output', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          uploadId: 'upload-2',
          uploadUrl: 'https://s3.example.com/upload2',
          formFields: [],
        }),
      });

      const result = await requestUpload(auth, {
        role: 'provider-output',
        taskId: 'task-123',
        fileName: 'chart.png',
        fileSize: 100000,
        mimeType: 'image/png',
      });

      expect(result.uploadId).toBe('upload-2');
      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.role).toBe('provider-output');
      expect(body.taskId).toBe('task-123');
    });

    it('throws on non-ok response', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        text: async () => 'Unauthorized',
      });

      await expect(
        requestUpload(auth, {
          role: 'consumer-input',
          agentName: 'test_agent',
          fileName: 'file.txt',
          fileSize: 100,
          mimeType: 'text/plain',
          partId: 'doc',
        }),
      ).rejects.toThrow('request-upload failed: HTTP 401');
    });

    it('includes protocol version and auth headers', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          uploadSessionId: 's1',
          uploadId: 'u1',
          uploadUrl: 'https://s3.example.com',
          formFields: [],
        }),
      });

      await requestUpload(auth, {
        role: 'consumer-input',
        agentName: 'test',
        fileName: 'f.txt',
        fileSize: 10,
        mimeType: 'text/plain',
        partId: 'p',
      });

      const headers = fetchSpy.mock.calls[0][1].headers;
      expect(headers['Blocks-Protocol-Version']).toBeDefined();
      expect(headers['Authorization']).toBe('Bearer test-jwt');
    });
  });

  describe('uploadToStorage', () => {
    it('sends multipart form data with form fields and file', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 204,
      });

      await uploadToStorage(
        'https://s3.example.com/upload',
        [
          { key: 'Policy', value: 'base64policy' },
          { key: 'key', value: '/path/to/file' },
        ],
        Buffer.from('file-content'),
        'test.txt',
        'text/plain',
      );

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe('https://s3.example.com/upload');
      expect(init.method).toBe('POST');
      // FormData sets the Content-Type + boundary on the fetch request
      // automatically; the SDK must not set it manually. The body is a
      // FormData instance carrying the form fields plus the "file" part.
      expect(init.headers).toBeUndefined();
      expect(init.body).toBeInstanceOf(FormData);
      const formBody = init.body as FormData;
      expect(formBody.get('Policy')).toBe('base64policy');
      expect(formBody.get('key')).toBe('/path/to/file');
      expect(formBody.get('file')).toBeInstanceOf(Blob);
    });

    it('throws on S3 error', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 400,
        text: async () => 'EntityTooLarge',
      });

      await expect(
        uploadToStorage(
          'https://s3.example.com/upload',
          [],
          Buffer.from('x'.repeat(30_000_000)),
          'big.bin',
          'application/octet-stream',
        ),
      ).rejects.toThrow('S3 upload failed: HTTP 400');
    });
  });

  describe('confirmUpload', () => {
    it('calls the confirm-upload endpoint', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ uploadId: 'upload-1' }),
      });

      const result = await confirmUpload(auth, 'upload-1');

      expect(result.uploadId).toBe('upload-1');
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe('http://localhost:3001/api/v1/files/confirm-upload');
      const body = JSON.parse(init.body);
      expect(body.uploadId).toBe('upload-1');
    });

    it('throws on non-ok response', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 409,
        text: async () => 'Conflict',
      });

      await expect(confirmUpload(auth, 'upload-1')).rejects.toThrow(
        'confirm-upload failed: HTTP 409',
      );
    });
  });

  describe('uploadFile (full flow)', () => {
    it('runs the complete consumer upload flow', async () => {
      // Step 1: request-upload
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          uploadSessionId: 'session-x',
          uploadId: 'upload-x',
          uploadUrl: 'https://s3.example.com/presigned',
          formFields: [{ key: 'Policy', value: 'pol' }],
        }),
      });
      // Step 2: S3 upload
      fetchSpy.mockResolvedValueOnce({ ok: true, status: 204 });
      // Step 3: confirm-upload
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ uploadId: 'upload-x' }),
      });

      const result = await uploadFile(
        auth,
        {
          role: 'consumer-input',
          agentName: 'test_agent',
          fileName: 'data.csv',
          fileSize: 50000,
          mimeType: 'text/csv',
          partId: 'dataset',
        },
        Buffer.alloc(50000),
      );

      expect(result.uploadSessionId).toBe('session-x');
      expect(result.uploadId).toBe('upload-x');
      expect(fetchSpy).toHaveBeenCalledTimes(3);
    });

    it('runs the complete provider upload flow', async () => {
      // Step 1: request-upload
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          uploadId: 'upload-p1',
          uploadUrl: 'https://s3.example.com/presigned',
          formFields: [],
        }),
      });
      // Step 2: S3 upload
      fetchSpy.mockResolvedValueOnce({ ok: true, status: 204 });
      // Step 3: confirm-upload (provider returns artifactRef)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          uploadId: 'upload-p1',
          artifactRef: {
            kind: 'file',
            channel: 'u.org1.task1',
            mimeType: 'image/png',
            size: 100000,
            fileId: 'pn-file-1',
            fileName: 'chart.png',
          },
        }),
      });

      const result = await uploadFile(
        auth,
        {
          role: 'provider-output',
          taskId: 'task-1',
          fileName: 'chart.png',
          fileSize: 100000,
          mimeType: 'image/png',
        },
        Buffer.alloc(100000),
      );

      expect(result.uploadId).toBe('upload-p1');
      expect(result.artifactRef).toBeDefined();
      expect(result.artifactRef!.kind).toBe('file');
      expect(result.artifactRef!.channel).toBe('u.org1.task1');
    });
  });

  describe('agentAuth integration', () => {
    it('uses agentAuth.authenticatedFetch when available', async () => {
      const authenticatedFetch = vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({
          uploadSessionId: 's1',
          uploadId: 'u1',
          uploadUrl: 'https://s3.example.com',
          formFields: [],
        }),
      });

      const authWithAgent: FileUploadAuth = {
        baseUrl: 'http://localhost:3001',
        agentAuth: { authenticatedFetch } as never,
      };

      await requestUpload(authWithAgent, {
        role: 'consumer-input',
        agentName: 'test',
        fileName: 'f.txt',
        fileSize: 10,
        mimeType: 'text/plain',
        partId: 'p',
      });

      expect(authenticatedFetch).toHaveBeenCalledTimes(1);
      expect(fetchSpy).not.toHaveBeenCalled();
    });
  });
});
