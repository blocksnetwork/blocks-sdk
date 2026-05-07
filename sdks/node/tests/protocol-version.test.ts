/**
 * Tests for protocol-version constants and helpers.
 */
import { describe, it, expect } from 'vitest';
import {
  CURRENT_PROTOCOL_VERSION,
  SUPPORTED_PROTOCOL_VERSIONS,
  DEPRECATED_PROTOCOL_VERSIONS,
  PROTOCOL_VERSION_HEADER,
  SDK_VERSION,
  isProtocolVersionSupported,
} from '../src/runtime/protocol-version.js';

describe('protocol-version module', () => {
  it('CURRENT_PROTOCOL_VERSION is a YYYY-MM-DD string', () => {
    expect(CURRENT_PROTOCOL_VERSION).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(CURRENT_PROTOCOL_VERSION).toBe('2026-05-01');
  });

  it('SUPPORTED_PROTOCOL_VERSIONS includes CURRENT_PROTOCOL_VERSION', () => {
    expect(SUPPORTED_PROTOCOL_VERSIONS).toContain(CURRENT_PROTOCOL_VERSION);
    expect(SUPPORTED_PROTOCOL_VERSIONS.length).toBeGreaterThanOrEqual(1);
  });

  it('DEPRECATED_PROTOCOL_VERSIONS is an array', () => {
    expect(Array.isArray(DEPRECATED_PROTOCOL_VERSIONS)).toBe(true);
  });

  it('PROTOCOL_VERSION_HEADER is the canonical header name', () => {
    expect(PROTOCOL_VERSION_HEADER).toBe('Blocks-Protocol-Version');
  });

  it('SDK_VERSION is a non-empty string', () => {
    expect(typeof SDK_VERSION).toBe('string');
    expect(SDK_VERSION.length).toBeGreaterThan(0);
  });

  describe('isProtocolVersionSupported', () => {
    it('returns true for a supported version', () => {
      expect(isProtocolVersionSupported(CURRENT_PROTOCOL_VERSION)).toBe(true);
    });

    it('returns false for an unsupported version', () => {
      expect(isProtocolVersionSupported('1999-01-01')).toBe(false);
    });

    it('returns false for empty string', () => {
      expect(isProtocolVersionSupported('')).toBe(false);
    });
  });
});
