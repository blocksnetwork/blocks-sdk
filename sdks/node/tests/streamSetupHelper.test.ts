import { describe, it, expect } from 'vitest';
import {
  parseStreamSetupResponse,
  parseStreamSetupError,
  extractFromPayload,
  extractErrorFromPayload,
} from '../src/runtime/stream-setup-helper.js';

// ---------------------------------------------------------------------------
// Test data helpers
// ---------------------------------------------------------------------------

function validPayload(overrides?: Record<string, unknown>): Record<string, unknown> {
  return {
    ok: true,
    streamSetupResponse: {
      taskId: 'task-123',
      streamId: 'temperature',
      channel: 'stream.weather.temperature',
      direction: 'outbound',
      phase: 'embedded',
      token: 'pam-token-t7a',
      tokenTtlMinutes: 62,
      ...overrides,
    },
  };
}

function wrapAsPubNubError(payload: unknown, statusCode = 403): Record<string, unknown> {
  return {
    status: {
      statusCode,
      errorData: {
        message: payload,
      },
    },
  };
}

// ---------------------------------------------------------------------------
// extractFromPayload tests
// ---------------------------------------------------------------------------

describe('extractFromPayload', () => {
  it('extracts valid embedded payload', () => {
    const result = extractFromPayload(validPayload());
    expect(result).not.toBeNull();
    expect(result!.taskId).toBe('task-123');
    expect(result!.streamId).toBe('temperature');
    expect(result!.channel).toBe('stream.weather.temperature');
    expect(result!.direction).toBe('outbound');
    expect(result!.phase).toBe('embedded');
    expect(result!.token).toBe('pam-token-t7a');
    expect(result!.tokenTtlMinutes).toBe(62);
  });

  it('extracts valid token_request payload', () => {
    const result = extractFromPayload(validPayload({ phase: 'token_request' }));
    expect(result).not.toBeNull();
    expect(result!.phase).toBe('token_request');
    expect(result!.token).toBe('pam-token-t7a');
  });

  it('extracts valid activate payload without token', () => {
    const payload = validPayload({ phase: 'activate' });
    delete (payload.streamSetupResponse as Record<string, unknown>).token;
    const result = extractFromPayload(payload);
    expect(result).not.toBeNull();
    expect(result!.phase).toBe('activate');
    expect(result!.token).toBeUndefined();
  });

  it('extracts bidirectional direction', () => {
    const result = extractFromPayload(validPayload({ direction: 'bidirectional' }));
    expect(result).not.toBeNull();
    expect(result!.direction).toBe('bidirectional');
  });

  it('extracts inbound direction', () => {
    const result = extractFromPayload(validPayload({ direction: 'inbound' }));
    expect(result).not.toBeNull();
    expect(result!.direction).toBe('inbound');
  });

  it('returns null when ok is false', () => {
    const payload = { ok: false, streamSetupResponse: validPayload().streamSetupResponse };
    expect(extractFromPayload(payload)).toBeNull();
  });

  it('returns null when ok is missing', () => {
    const payload = { streamSetupResponse: validPayload().streamSetupResponse };
    expect(extractFromPayload(payload)).toBeNull();
  });

  it('returns null when streamSetupResponse is missing', () => {
    expect(extractFromPayload({ ok: true })).toBeNull();
  });

  it('returns null when streamSetupResponse is not an object', () => {
    expect(extractFromPayload({ ok: true, streamSetupResponse: 'string' })).toBeNull();
  });

  it('returns null for null input', () => {
    expect(extractFromPayload(null)).toBeNull();
  });

  it('returns null for undefined input', () => {
    expect(extractFromPayload(undefined)).toBeNull();
  });

  it('returns null for non-object input', () => {
    expect(extractFromPayload('string')).toBeNull();
    expect(extractFromPayload(42)).toBeNull();
  });

  it('returns null when required fields are missing', () => {
    // Missing taskId
    const p1 = validPayload();
    delete (p1.streamSetupResponse as Record<string, unknown>).taskId;
    expect(extractFromPayload(p1)).toBeNull();

    // Missing streamId
    const p2 = validPayload();
    delete (p2.streamSetupResponse as Record<string, unknown>).streamId;
    expect(extractFromPayload(p2)).toBeNull();

    // Missing channel
    const p3 = validPayload();
    delete (p3.streamSetupResponse as Record<string, unknown>).channel;
    expect(extractFromPayload(p3)).toBeNull();

    // Missing direction
    const p4 = validPayload();
    delete (p4.streamSetupResponse as Record<string, unknown>).direction;
    expect(extractFromPayload(p4)).toBeNull();

    // Missing phase
    const p5 = validPayload();
    delete (p5.streamSetupResponse as Record<string, unknown>).phase;
    expect(extractFromPayload(p5)).toBeNull();

    // Missing tokenTtlMinutes
    const p6 = validPayload();
    delete (p6.streamSetupResponse as Record<string, unknown>).tokenTtlMinutes;
    expect(extractFromPayload(p6)).toBeNull();
  });

  it('returns null for invalid direction', () => {
    expect(extractFromPayload(validPayload({ direction: 'upstream' }))).toBeNull();
  });

  it('returns null for invalid phase', () => {
    expect(extractFromPayload(validPayload({ phase: 'unknown' }))).toBeNull();
  });

  it('returns null for zero tokenTtlMinutes', () => {
    expect(extractFromPayload(validPayload({ tokenTtlMinutes: 0 }))).toBeNull();
  });

  it('returns null for negative tokenTtlMinutes', () => {
    expect(extractFromPayload(validPayload({ tokenTtlMinutes: -5 }))).toBeNull();
  });

  it('returns null for empty string fields', () => {
    expect(extractFromPayload(validPayload({ taskId: '' }))).toBeNull();
    expect(extractFromPayload(validPayload({ streamId: '' }))).toBeNull();
    expect(extractFromPayload(validPayload({ channel: '' }))).toBeNull();
  });

  it('omits token when empty string', () => {
    const result = extractFromPayload(validPayload({ token: '' }));
    expect(result).not.toBeNull();
    expect(result!.token).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// parseStreamSetupResponse tests (PubNub error structure)
// ---------------------------------------------------------------------------

describe('parseStreamSetupResponse', () => {
  it('extracts from valid PubNub 403 error', () => {
    const error = wrapAsPubNubError(validPayload());
    const result = parseStreamSetupResponse(error);
    expect(result).not.toBeNull();
    expect(result!.taskId).toBe('task-123');
    expect(result!.streamId).toBe('temperature');
    expect(result!.token).toBe('pam-token-t7a');
  });

  it('returns null for non-403 status code', () => {
    const error = wrapAsPubNubError(validPayload(), 401);
    expect(parseStreamSetupResponse(error)).toBeNull();
  });

  it('returns null when status is missing', () => {
    expect(parseStreamSetupResponse({})).toBeNull();
  });

  it('returns null when errorData is missing', () => {
    const error = { status: { statusCode: 403 } };
    expect(parseStreamSetupResponse(error)).toBeNull();
  });

  it('returns null when message is not an object', () => {
    const error = { status: { statusCode: 403, errorData: { message: 'string error' } } };
    expect(parseStreamSetupResponse(error)).toBeNull();
  });

  it('returns null for null input', () => {
    expect(parseStreamSetupResponse(null)).toBeNull();
  });

  it('returns null for undefined input', () => {
    expect(parseStreamSetupResponse(undefined)).toBeNull();
  });

  it('returns null for non-object input', () => {
    expect(parseStreamSetupResponse('error string')).toBeNull();
  });

  it('correctly handles real 403 error (no marker)', () => {
    const error = wrapAsPubNubError({ error: 'Forbidden' });
    expect(parseStreamSetupResponse(error)).toBeNull();
  });

  it('correctly handles 403 with ok:false', () => {
    const payload = { ok: false, streamSetupResponse: validPayload().streamSetupResponse };
    const error = wrapAsPubNubError(payload);
    expect(parseStreamSetupResponse(error)).toBeNull();
  });

  it('extracts token_request phase from error', () => {
    const payload = validPayload({ phase: 'token_request' });
    const error = wrapAsPubNubError(payload);
    const result = parseStreamSetupResponse(error);
    expect(result).not.toBeNull();
    expect(result!.phase).toBe('token_request');
  });

  it('extracts activate phase without token from error', () => {
    const payload = validPayload({ phase: 'activate' });
    delete (payload.streamSetupResponse as Record<string, unknown>).token;
    const error = wrapAsPubNubError(payload);
    const result = parseStreamSetupResponse(error);
    expect(result).not.toBeNull();
    expect(result!.phase).toBe('activate');
    expect(result!.token).toBeUndefined();
  });

  it('handles all three directions via error path', () => {
    for (const direction of ['outbound', 'inbound', 'bidirectional']) {
      const error = wrapAsPubNubError(validPayload({ direction }));
      const result = parseStreamSetupResponse(error);
      expect(result).not.toBeNull();
      expect(result!.direction).toBe(direction);
    }
  });
});

// ---------------------------------------------------------------------------
// extractErrorFromPayload tests
// ---------------------------------------------------------------------------

describe('extractErrorFromPayload', () => {
  it('extracts valid error payload', () => {
    const payload = {
      ok: false,
      error: { code: 'InvalidArgument', message: 'durationMinutes is required and must be a positive number' },
    };
    const result = extractErrorFromPayload(payload);
    expect(result).not.toBeNull();
    expect(result!.code).toBe('InvalidArgument');
    expect(result!.message).toBe('durationMinutes is required and must be a positive number');
  });

  it('extracts TokenGrantFailed error', () => {
    const payload = {
      ok: false,
      error: { code: 'TokenGrantFailed', message: 'Failed to grant tokens for channel stream.weather.temp' },
    };
    const result = extractErrorFromPayload(payload);
    expect(result).not.toBeNull();
    expect(result!.code).toBe('TokenGrantFailed');
    expect(result!.message).toBe('Failed to grant tokens for channel stream.weather.temp');
  });

  it('returns null for success payload (ok: true)', () => {
    expect(extractErrorFromPayload(validPayload())).toBeNull();
  });

  it('returns null when ok is missing', () => {
    expect(extractErrorFromPayload({ error: { code: 'X', message: 'Y' } })).toBeNull();
  });

  it('returns null when error is missing', () => {
    expect(extractErrorFromPayload({ ok: false })).toBeNull();
  });

  it('returns null when error is not an object', () => {
    expect(extractErrorFromPayload({ ok: false, error: 'string' })).toBeNull();
  });

  it('returns null when code is missing', () => {
    expect(extractErrorFromPayload({ ok: false, error: { message: 'msg' } })).toBeNull();
  });

  it('returns null when message is missing', () => {
    expect(extractErrorFromPayload({ ok: false, error: { code: 'X' } })).toBeNull();
  });

  it('returns null when code is empty string', () => {
    expect(extractErrorFromPayload({ ok: false, error: { code: '', message: 'msg' } })).toBeNull();
  });

  it('returns null when message is empty string', () => {
    expect(extractErrorFromPayload({ ok: false, error: { code: 'X', message: '' } })).toBeNull();
  });

  it('returns null for null input', () => {
    expect(extractErrorFromPayload(null)).toBeNull();
  });

  it('returns null for undefined input', () => {
    expect(extractErrorFromPayload(undefined)).toBeNull();
  });

  it('returns null for non-object input', () => {
    expect(extractErrorFromPayload('string')).toBeNull();
    expect(extractErrorFromPayload(42)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// parseStreamSetupError tests (PubNub error structure)
// ---------------------------------------------------------------------------

describe('parseStreamSetupError', () => {
  it('extracts error from 403 with ok:false error payload', () => {
    const payload = {
      ok: false,
      error: { code: 'InvalidArgument', message: 'durationMinutes is required and must be a positive number' },
    };
    const error = wrapAsPubNubError(payload);
    const result = parseStreamSetupError(error);
    expect(result).not.toBeNull();
    expect(result!.code).toBe('InvalidArgument');
    expect(result!.message).toBe('durationMinutes is required and must be a positive number');
  });

  it('extracts TokenGrantFailed error from 403', () => {
    const payload = {
      ok: false,
      error: { code: 'TokenGrantFailed', message: 'Failed to grant agent token for channel stream.weather.temp' },
    };
    const error = wrapAsPubNubError(payload);
    const result = parseStreamSetupError(error);
    expect(result).not.toBeNull();
    expect(result!.code).toBe('TokenGrantFailed');
  });

  it('returns null for success abort payload (ok: true)', () => {
    const error = wrapAsPubNubError(validPayload());
    expect(parseStreamSetupError(error)).toBeNull();
  });

  it('returns null for real 403 error (no ok field)', () => {
    const error = wrapAsPubNubError({ error: 'Forbidden' });
    expect(parseStreamSetupError(error)).toBeNull();
  });

  it('returns null for non-403 status code', () => {
    const payload = {
      ok: false,
      error: { code: 'InvalidArgument', message: 'test' },
    };
    const error = wrapAsPubNubError(payload, 401);
    expect(parseStreamSetupError(error)).toBeNull();
  });

  it('returns null when status is missing', () => {
    expect(parseStreamSetupError({})).toBeNull();
  });

  it('returns null when errorData is missing', () => {
    expect(parseStreamSetupError({ status: { statusCode: 403 } })).toBeNull();
  });

  it('returns null for null input', () => {
    expect(parseStreamSetupError(null)).toBeNull();
  });

  it('returns null for undefined input', () => {
    expect(parseStreamSetupError(undefined)).toBeNull();
  });

  it('error payload is NOT parsed as success by parseStreamSetupResponse', () => {
    const payload = {
      ok: false,
      error: { code: 'InvalidArgument', message: 'Missing required fields' },
    };
    const error = wrapAsPubNubError(payload);
    // parseStreamSetupResponse should return null for error payloads
    expect(parseStreamSetupResponse(error)).toBeNull();
    // parseStreamSetupError should extract the error
    const setupError = parseStreamSetupError(error);
    expect(setupError).not.toBeNull();
    expect(setupError!.code).toBe('InvalidArgument');
    expect(setupError!.message).toBe('Missing required fields');
  });

  it('success payload is NOT parsed as error by parseStreamSetupError', () => {
    const error = wrapAsPubNubError(validPayload());
    // parseStreamSetupError should return null for success payloads
    expect(parseStreamSetupError(error)).toBeNull();
    // parseStreamSetupResponse should extract the result
    const result = parseStreamSetupResponse(error);
    expect(result).not.toBeNull();
    expect(result!.taskId).toBe('task-123');
  });
});
