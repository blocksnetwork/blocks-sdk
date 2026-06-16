import { describe, it, expect } from 'vitest';

import {
  validateSuccessEnvelope,
  validateErrorEnvelope,
} from '../src/schemas.js';

const validSuccess = () => ({
  type: 'blocks-auth-success' as const,
  version: 1 as const,
  state: 'a'.repeat(22),
  jwt: 'header.payload.sig',
  refreshToken: 'r'.repeat(40),
  expiresAt: 1_700_000_000_000,
  agentIds: ['11111111-1111-1111-1111-111111111111'],
  agents: [
    {
      name: 'translator',
      id: '11111111-1111-1111-1111-111111111111',
      billingMode: 'free' as const,
    },
  ],
  orgId: '22222222-2222-2222-2222-222222222222',
  userId: '33333333-3333-3333-3333-333333333333',
});

const validError = (overrides: Record<string, unknown> = {}) => ({
  type: 'blocks-auth-error',
  version: 1,
  state: 'a'.repeat(22),
  code: 'USER_CANCELLED',
  message: 'User cancelled',
  ...overrides,
});

describe('validateSuccessEnvelope', () => {
  it('accepts a well-formed v1 success envelope', () => {
    expect(validateSuccessEnvelope(validSuccess())).toBe(true);
  });

  it('rejects when `state` is missing', () => {
    const env = validSuccess() as Record<string, unknown>;
    delete env.state;
    expect(validateSuccessEnvelope(env)).toBe(false);
  });

  it('rejects when `version` is wrong', () => {
    const env = { ...validSuccess(), version: 2 };
    expect(validateSuccessEnvelope(env)).toBe(false);
  });

  it('rejects when `state` is too short (<22 chars)', () => {
    const env = { ...validSuccess(), state: 'short' };
    expect(validateSuccessEnvelope(env)).toBe(false);
  });

  it('rejects when `agentIds` contains a non-UUID string', () => {
    const env = { ...validSuccess(), agentIds: ['not-a-uuid'] };
    expect(validateSuccessEnvelope(env)).toBe(false);
  });

  it('rejects when `agents[i].id` is not a UUID', () => {
    const env = validSuccess();
    env.agents[0]!.id = 'not-a-uuid';
    expect(validateSuccessEnvelope(env)).toBe(false);
  });

  it('rejects an unknown additional property', () => {
    const env = { ...validSuccess(), extra: 'banned' };
    expect(validateSuccessEnvelope(env)).toBe(false);
  });

  it('rejects an error-shaped envelope', () => {
    expect(validateSuccessEnvelope(validError())).toBe(false);
  });
});

describe('validateErrorEnvelope', () => {
  it('accepts a well-formed v1 error envelope (with `message`)', () => {
    expect(validateErrorEnvelope(validError())).toBe(true);
  });

  it('accepts the per-agent shape (with `agent`)', () => {
    const env = validError({ code: 'AGENT_DISABLED', agent: 'translator' });
    expect(validateErrorEnvelope(env)).toBe(true);
  });

  it('rejects when `message` is missing (post-fix shape requires it)', () => {
    const env = validError() as Record<string, unknown>;
    delete env.message;
    expect(validateErrorEnvelope(env)).toBe(false);
  });

  it('rejects an unknown error code', () => {
    const env = validError({ code: 'NOT_A_REAL_CODE' });
    expect(validateErrorEnvelope(env)).toBe(false);
  });

  it('rejects when `state` is missing', () => {
    const env = validError() as Record<string, unknown>;
    delete env.state;
    expect(validateErrorEnvelope(env)).toBe(false);
  });

  it('rejects an unknown additional property', () => {
    const env = validError({ extra: 'banned' });
    expect(validateErrorEnvelope(env)).toBe(false);
  });

  it('rejects a success-shaped envelope', () => {
    expect(validateErrorEnvelope(validSuccess())).toBe(false);
  });
});
