import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  detectDeviceOs,
  connectAgent,
} from '../src/runtime/agent-registry.js';

const TEST_BASE_URL = 'http://test-api.example.com';
const TEST_AGENT_NAME = 'test_env_agent';
const TEST_INSTANCE_ID = 'AG-test_env_agent-abc123';

/** Known values returned by Node.js os.platform() */
const KNOWN_PLATFORMS = [
  'aix',
  'android',
  'darwin',
  'freebsd',
  'haiku',
  'linux',
  'openbsd',
  'sunos',
  'win32',
  'cygwin',
  'netbsd',
];

describe('detectDeviceOs()', () => {
  it('returns a non-empty string', () => {
    const result = detectDeviceOs();
    expect(typeof result).toBe('string');
    expect(result.length).toBeGreaterThan(0);
  });

  it('returns a known Node.js platform value or "unknown"', () => {
    const result = detectDeviceOs();
    const valid = [...KNOWN_PLATFORMS, 'unknown'];
    expect(valid).toContain(result);
  });
});

describe('registration payload includes deviceOs and sdkLanguage', () => {
  function mockAgentAuth() {
    return { init: vi.fn().mockResolvedValue({ pamToken: 'pam-test' }) };
  }

  it('sends deviceOs and sdkLanguage in the registration payload', async () => {
    const auth = mockAgentAuth();

    await connectAgent(TEST_AGENT_NAME, {
      instanceId: TEST_INSTANCE_ID,
      description: 'Environment detection test agent',
      baseUrl: TEST_BASE_URL,
      agentAuth: auth as any,
    });

    expect(auth.init).toHaveBeenCalledTimes(1);
    const payload = auth.init.mock.calls[0][0];

    expect(payload.deviceOs).toBe(detectDeviceOs());
    expect(payload.sdkLanguage).toBe('Node');
  });

  it('deviceOs is a non-empty string in the payload', async () => {
    const auth = mockAgentAuth();

    await connectAgent(TEST_AGENT_NAME, {
      instanceId: TEST_INSTANCE_ID,
      baseUrl: TEST_BASE_URL,
      agentAuth: auth as any,
    });

    const payload = auth.init.mock.calls[0][0];
    expect(typeof payload.deviceOs).toBe('string');
    expect(payload.deviceOs.length).toBeGreaterThan(0);
  });

  it('sdkLanguage is exactly "Node" in the payload', async () => {
    const auth = mockAgentAuth();

    await connectAgent(TEST_AGENT_NAME, {
      instanceId: TEST_INSTANCE_ID,
      baseUrl: TEST_BASE_URL,
      agentAuth: auth as any,
    });

    const payload = auth.init.mock.calls[0][0];
    expect(payload.sdkLanguage).toBe('Node');
  });
});
