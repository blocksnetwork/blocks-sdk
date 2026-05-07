/**
 * Stream Setup Helper - T7a abort-payload parsing
 *
 * Internal helper for consuming the streamSetup Function's response.
 * The streamSetup Function returns T7a via request.abort(customPayload),
 * which the PubNub SDK surfaces as a 403 error. This helper extracts
 * the T7a token from the error body after verifying the expected markers.
 *
 * This is a protocol-consumption helper only. It does not implement the
 * full stream setup handshake (that belongs to Phase 3 SDK runtime).
 *
 * The stream setup protocol is documented in the SDK contract and event flow docs.
 */

/**
 * Parsed stream setup response extracted from the abort payload.
 */
export interface StreamSetupResult {
  taskId: string;
  streamId: string;
  channel: string;
  direction: 'outbound' | 'inbound' | 'bidirectional';
  phase: 'embedded' | 'token_request' | 'activate';
  token?: string;
  tokenTtlMinutes: number;
}

/**
 * Structured error returned by the streamSetup Function for validation
 * failures. The Function returns { ok: false, error: { code, message } }
 * via request.abort(), which is also surfaced as a 403 error. This type
 * lets callers distinguish a server-side validation rejection from an
 * opaque PubNub 403.
 */
export interface StreamSetupError {
  code: string;
  message: string;
}

/**
 * Shape of the abort payload returned by the streamSetup Function.
 * The PubNub SDK wraps this in an error object.
 */
interface AbortPayload {
  ok: boolean;
  streamSetupResponse: {
    taskId: string;
    streamId: string;
    channel: string;
    direction: string;
    phase: string;
    token?: string;
    tokenTtlMinutes: number;
  };
}

const VALID_DIRECTIONS = ['outbound', 'inbound', 'bidirectional'] as const;
const VALID_PHASES = ['embedded', 'token_request', 'activate'] as const;

/**
 * Attempt to extract a structured error from a PubNub publish error.
 *
 * When the streamSetup Function rejects a request (e.g., missing
 * durationMinutes, invalid direction), it returns
 * { ok: false, error: { code, message } } via request.abort().
 * This is surfaced as a 403 by the PubNub SDK, just like the success
 * path. This function extracts the structured error from the 403 body.
 *
 * Returns null if the error is not a structured setup error (i.e., it is
 * either a real 403 or a success abort payload).
 *
 * @param error - The error object thrown by pubnub.publish()
 * @returns The parsed StreamSetupError, or null
 */
export function parseStreamSetupError(error: unknown): StreamSetupError | null {
  if (!error || typeof error !== 'object') {
    return null;
  }

  const status = (error as Record<string, unknown>).status as Record<string, unknown> | undefined;
  if (!status) {
    return null;
  }

  const statusCode = status.statusCode ?? status.category;
  if (statusCode !== 403) {
    return null;
  }

  const errorData = status.errorData as Record<string, unknown> | undefined;
  if (!errorData) {
    return null;
  }

  const payload = errorData.message as Record<string, unknown> | undefined;
  if (!payload || typeof payload !== 'object') {
    return null;
  }

  return extractErrorFromPayload(payload);
}

/**
 * Attempt to parse a T7a stream setup response from a PubNub publish error.
 *
 * The streamSetup Function calls request.abort(customPayload), which causes
 * the PubNub SDK to reject the publish with a PubNubError. The error
 * object contains the custom payload at:
 *   error.status.errorData.message  (already parsed by the Node SDK)
 *
 * This function checks the marker fields (ok: true, streamSetupResponse)
 * and extracts the result. Returns null if the error is not a valid
 * stream setup response (i.e., it is a real 403 error).
 *
 * @param error - The error object thrown by pubnub.publish()
 * @returns The parsed StreamSetupResult, or null if not a valid setup response
 */
export function parseStreamSetupResponse(error: unknown): StreamSetupResult | null {
  if (!error || typeof error !== 'object') {
    return null;
  }

  // Navigate the PubNub Node SDK error structure:
  // error.status.statusCode === 403
  // error.status.errorData.message === { ok: true, streamSetupResponse: {...} }
  const status = (error as Record<string, unknown>).status as Record<string, unknown> | undefined;
  if (!status) {
    return null;
  }

  const statusCode = status.statusCode ?? status.category;
  if (statusCode !== 403) {
    return null;
  }

  const errorData = status.errorData as Record<string, unknown> | undefined;
  if (!errorData) {
    return null;
  }

  const payload = errorData.message as AbortPayload | undefined;
  if (!payload || typeof payload !== 'object') {
    return null;
  }

  return extractFromPayload(payload);
}

/**
 * Extract a structured error from a raw abort payload object.
 * The streamSetup Function returns { ok: false, error: { code, message } }
 * for validation failures (missing fields, invalid direction, invalid
 * durationMinutes, etc.). This function checks for that shape and
 * returns the error details, or null if the payload is not a structured error.
 *
 * @param payload - The parsed abort payload
 * @returns The parsed StreamSetupError, or null if not an error payload
 */
export function extractErrorFromPayload(payload: unknown): StreamSetupError | null {
  if (!payload || typeof payload !== 'object') {
    return null;
  }

  const obj = payload as Record<string, unknown>;

  // Error payloads have ok === false and an error object
  if (obj.ok !== false) {
    return null;
  }

  const error = obj.error;
  if (!error || typeof error !== 'object') {
    return null;
  }

  const err = error as Record<string, unknown>;
  const code = err.code;
  const message = err.message;

  if (typeof code !== 'string' || !code) {
    return null;
  }
  if (typeof message !== 'string' || !message) {
    return null;
  }

  return { code, message };
}

/**
 * Extract StreamSetupResult from a raw abort payload object.
 * Validates the marker fields and required properties.
 *
 * @param payload - The parsed abort payload
 * @returns The parsed StreamSetupResult, or null if invalid
 */
export function extractFromPayload(payload: unknown): StreamSetupResult | null {
  if (!payload || typeof payload !== 'object') {
    return null;
  }

  const obj = payload as Record<string, unknown>;

  // Check marker fields
  if (obj.ok !== true) {
    return null;
  }

  const response = obj.streamSetupResponse;
  if (!response || typeof response !== 'object') {
    return null;
  }

  const resp = response as Record<string, unknown>;

  // Validate required fields
  const taskId = resp.taskId;
  const streamId = resp.streamId;
  const channel = resp.channel;
  const direction = resp.direction;
  const phase = resp.phase;
  const tokenTtlMinutes = resp.tokenTtlMinutes;

  if (typeof taskId !== 'string' || !taskId) return null;
  if (typeof streamId !== 'string' || !streamId) return null;
  if (typeof channel !== 'string' || !channel) return null;
  if (typeof direction !== 'string' || !(VALID_DIRECTIONS as readonly string[]).includes(direction)) return null;
  if (typeof phase !== 'string' || !(VALID_PHASES as readonly string[]).includes(phase)) return null;
  if (typeof tokenTtlMinutes !== 'number' || tokenTtlMinutes <= 0) return null;

  const result: StreamSetupResult = {
    taskId,
    streamId,
    channel,
    direction: direction as StreamSetupResult['direction'],
    phase: phase as StreamSetupResult['phase'],
    tokenTtlMinutes,
  };

  // Token is present for embedded and token_request phases, absent for activate
  const token = resp.token;
  if (typeof token === 'string' && token.length > 0) {
    result.token = token;
  }

  return result;
}
