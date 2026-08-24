/**
 * Shared pre-signed URL upload helper for task artifacts.
 *
 * Implements the three-step upload handshake:
 *   1. request-upload  -> Backend returns uploadUrl, formFields, uploadId
 *   2. S3 multipart POST -> SDK uploads directly to PubNub Files storage
 *   3. confirm-upload  -> Backend validates and returns result
 *
 * Used by both consumer input (task-client.ts) and provider output
 * (agent-instance.ts). File bytes never flow through the backend.
 */

import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER } from './protocol-version.js';
import type { AgentAuth } from './agent-auth.js';
import type { AuthProvider } from './auth-provider.js';
import { preflightAuthOrThrow } from './auth-provider.js';
import { captureAffinity, injectAffinity } from './write-affinity.js';

// ============================================================================
// Types
// ============================================================================

/** Fields returned by the backend for direct S3 upload. */
export interface UploadFormField {
  key: string;
  value: string;
}

/** Response from POST /api/v1/files/request-upload (consumer input). */
export interface RequestUploadResponseConsumer {
  uploadSessionId: string;
  uploadId: string;
  uploadUrl: string;
  formFields: UploadFormField[];
}

/** Response from POST /api/v1/files/request-upload (provider output). */
export interface RequestUploadResponseProvider {
  uploadId: string;
  uploadUrl: string;
  formFields: UploadFormField[];
}

/** Response from POST /api/v1/files/confirm-upload (consumer input). */
export interface ConfirmUploadResponseConsumer {
  uploadId: string;
}

/** Response from POST /api/v1/files/confirm-upload (provider output). */
export interface ConfirmUploadResponseProvider {
  uploadId: string;
  artifactRef: {
    kind: 'file';
    channel: string;
    mimeType: string;
    size: number;
    fileId: string;
    fileName: string;
    hash?: string;
    expiresAt?: string;
  };
}

/** Parameters for a consumer-input request-upload call. */
export interface ConsumerUploadParams {
  role: 'consumer-input';
  agentName: string;
  fileName: string;
  fileSize: number;
  mimeType: string;
  partId: string;
  uploadSessionId?: string;
}

/** Parameters for a provider-output request-upload call. */
export interface ProviderUploadParams {
  role: 'provider-output';
  taskId: string;
  fileName: string;
  fileSize: number;
  mimeType: string;
  outputId?: string;
}

/** Auth context for making backend HTTP calls. */
export interface FileUploadAuth {
  baseUrl: string;
  authProvider?: AuthProvider;
  agentAuth?: AgentAuth;
}

// ============================================================================
// Internal helpers
// ============================================================================

/**
 * Make an authenticated HTTP request to the backend.
 * Uses AgentAuth.authenticatedFetch when available, otherwise plain fetch
 * with AuthProvider header. Handles 401 reactive refresh for authProvider.
 */
async function backendFetch(
  auth: FileUploadAuth,
  path: string,
  body: Record<string, unknown>,
): Promise<Response> {
  // Pre-flight: when the auth provider has a recorded permanent-refresh
  // error, attempt one reactive recovery. On failure the typed
  // AuthRefreshFailedError is thrown so file uploads surface it instead
  // of an opaque 401 from the request-upload / confirm-upload endpoints.
  await preflightAuthOrThrow(auth.authProvider);

  const url = `${auth.baseUrl.replace(/\/+$/, '')}${path}`;

  // agentAuth injects/captures affinity inside authenticatedFetch; the
  // non-agentAuth path below handles it directly.
  //
  // `credentials: 'omit'` keeps the browser from auto-attaching the
  // dashboard session cookie when the SDK and backend share an origin
  // (e.g. https://app.blocks.ai). Without this, the backend's auth
  // middleware resolves via the cookie path before reaching the Bearer
  // path, populates `req.session`, and the CSRF middleware then rejects
  // the call as `CSRF_INVALID`. The Bearer JWT in `Authorization` is
  // the SDK's auth and is not CSRF-vulnerable. Node fetch has no cookie
  // jar, so this is a no-op in provider contexts.
  const buildInit = (includeAffinity: boolean): RequestInit => {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    };
    const authHeader = auth.authProvider?.getAuthHeader();
    if (authHeader) {
      headers['Authorization'] = authHeader;
    }
    if (includeAffinity) injectAffinity(headers);
    return {
      method: 'POST',
      credentials: 'omit',
      headers,
      body: JSON.stringify(body),
    };
  };

  if (auth.agentAuth) {
    return auth.agentAuth.authenticatedFetch(url, buildInit(false));
  }

  const response = await fetch(url, buildInit(true));
  captureAffinity(response.headers);

  // 401 reactive refresh for authProvider
  if (response.status === 401 && auth.authProvider) {
    const refreshed = await auth.authProvider.onAuthFailure();
    if (refreshed) {
      const retryResponse = await fetch(url, buildInit(true));
      captureAffinity(retryResponse.headers);
      return retryResponse;
    }
  }

  return response;
}

// ============================================================================
// Step 1: Request Upload
// ============================================================================

/**
 * Request a pre-signed upload URL from the backend.
 * Returns the upload URL, form fields, upload ID, and (for consumer input)
 * the upload session ID.
 */
export async function requestUpload(
  auth: FileUploadAuth,
  params: ConsumerUploadParams,
): Promise<RequestUploadResponseConsumer>;
export async function requestUpload(
  auth: FileUploadAuth,
  params: ProviderUploadParams,
): Promise<RequestUploadResponseProvider>;
export async function requestUpload(
  auth: FileUploadAuth,
  params: ConsumerUploadParams | ProviderUploadParams,
): Promise<RequestUploadResponseConsumer | RequestUploadResponseProvider> {
  const response = await backendFetch(auth, '/api/v1/files/request-upload', params as unknown as Record<string, unknown>);

  if (!response.ok) {
    const text = await response.text().catch(() => '');
    throw new Error(`request-upload failed: HTTP ${response.status} ${text}`);
  }

  return response.json() as Promise<RequestUploadResponseConsumer | RequestUploadResponseProvider>;
}

// ============================================================================
// Step 2: Direct S3 Upload
// ============================================================================

/**
 * Upload file data directly to PubNub Files storage via S3 pre-signed URL.
 * Constructs a multipart/form-data body with all form fields first,
 * then appends the file as the last field named "file".
 *
 * Uses the native `FormData` + `Blob` types available in both Node 18+
 * and all modern browsers. `fetch` computes the multipart boundary
 * automatically -- do NOT set `Content-Type` manually.
 *
 * Returns void on success (S3 returns 204 No Content).
 * Throws on failure (e.g., 400 EntityTooLarge for oversized files).
 */
export async function uploadToStorage(
  uploadUrl: string,
  formFields: UploadFormField[],
  fileData: Uint8Array | ArrayBuffer | Blob,
  fileName: string,
  mimeType: string,
): Promise<void> {
  const formData = new FormData();

  // Pre-signed URL form fields first (order matters for some S3 configs).
  for (const field of formFields) {
    formData.append(field.key, field.value);
  }

  // File as the last field, named "file" (required by S3 pre-signed POST).
  // The `as never` narrows away TypeScript's stricter `BlobPart`
  // constraint (which excludes the runtime-permissive
  // `Uint8Array<ArrayBufferLike>`). At runtime the Blob constructor
  // accepts any ArrayBufferView or ArrayBuffer; TS 5.4's lib typings
  // are conservative.
  const blob =
    fileData instanceof Blob
      ? fileData
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      : new Blob([fileData as any], { type: mimeType });
  formData.append('file', blob, fileName);

  const response = await fetch(uploadUrl, {
    method: 'POST',
    body: formData,
    // Do NOT set Content-Type -- fetch sets the multipart boundary
    // automatically when body is FormData.
  });

  if (!response.ok) {
    const text = await response.text().catch(() => '');
    throw new Error(`S3 upload failed: HTTP ${response.status} ${text}`);
  }
}

// ============================================================================
// Step 3: Confirm Upload
// ============================================================================

/**
 * Confirm a completed upload with the backend.
 *
 * For consumer input: returns { uploadId }.
 * For provider output: the backend publishes the typed artifact event
 * on the task channel and returns { uploadId, artifactRef }.
 */
export async function confirmUpload(
  auth: FileUploadAuth,
  uploadId: string,
): Promise<ConfirmUploadResponseConsumer>;
export async function confirmUpload(
  auth: FileUploadAuth,
  uploadId: string,
  expectArtifactRef: true,
): Promise<ConfirmUploadResponseProvider>;
export async function confirmUpload(
  auth: FileUploadAuth,
  uploadId: string,
  expectArtifactRef?: boolean,
): Promise<ConfirmUploadResponseConsumer | ConfirmUploadResponseProvider> {
  void expectArtifactRef; // used only for type narrowing at callsites
  const response = await backendFetch(auth, '/api/v1/files/confirm-upload', { uploadId });

  if (!response.ok) {
    const text = await response.text().catch(() => '');
    throw new Error(`confirm-upload failed: HTTP ${response.status} ${text}`);
  }

  return response.json() as Promise<ConfirmUploadResponseConsumer | ConfirmUploadResponseProvider>;
}

// ============================================================================
// Full Upload Flow (convenience wrapper)
// ============================================================================

/**
 * Run the complete three-step upload flow for a single file.
 * Shared by consumer input and provider output paths.
 */
export async function uploadFile(
  auth: FileUploadAuth,
  params: ConsumerUploadParams,
  fileData: Uint8Array | ArrayBuffer | Blob,
): Promise<{ uploadSessionId: string; uploadId: string }>;
export async function uploadFile(
  auth: FileUploadAuth,
  params: ProviderUploadParams,
  fileData: Uint8Array | ArrayBuffer | Blob,
): Promise<ConfirmUploadResponseProvider>;
export async function uploadFile(
  auth: FileUploadAuth,
  params: ConsumerUploadParams | ProviderUploadParams,
  fileData: Uint8Array | ArrayBuffer | Blob,
): Promise<{ uploadSessionId?: string; uploadId: string; artifactRef?: ConfirmUploadResponseProvider['artifactRef'] }> {
  // Step 1: Request upload URL
  const uploadResponse = await requestUpload(auth, params as ConsumerUploadParams);

  // Step 2: Upload directly to storage
  await uploadToStorage(
    uploadResponse.uploadUrl,
    uploadResponse.formFields,
    fileData,
    params.fileName,
    params.mimeType,
  );

  // Step 3: Confirm upload
  if (params.role === 'provider-output') {
    const confirmResult = await confirmUpload(auth, uploadResponse.uploadId, true);
    return confirmResult;
  }

  const consumerResponse = uploadResponse as RequestUploadResponseConsumer;
  await confirmUpload(auth, uploadResponse.uploadId);
  return {
    uploadSessionId: consumerResponse.uploadSessionId,
    uploadId: uploadResponse.uploadId,
  };
}
