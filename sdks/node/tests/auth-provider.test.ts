/**
 * Tests for AuthProvider interface and StaticAuthProvider implementation.
 */
import { describe, it, expect } from 'vitest';
import { StaticAuthProvider, type AuthProvider } from '../src/runtime/auth-provider.js';

describe('StaticAuthProvider', () => {
  it('returns Bearer header from getAuthHeader()', () => {
    const provider = new StaticAuthProvider('my-jwt-token');
    expect(provider.getAuthHeader()).toBe('Bearer my-jwt-token');
  });

  it('returns false from onAuthFailure() (no refresh capability)', async () => {
    const provider = new StaticAuthProvider('my-jwt-token');
    const result = await provider.onAuthFailure();
    expect(result).toBe(false);
  });

  it('implements AuthProvider interface', () => {
    const provider: AuthProvider = new StaticAuthProvider('token');
    expect(typeof provider.getAuthHeader).toBe('function');
    expect(typeof provider.onAuthFailure).toBe('function');
  });

  it('returns consistent header on repeated calls', () => {
    const provider = new StaticAuthProvider('stable-token');
    expect(provider.getAuthHeader()).toBe('Bearer stable-token');
    expect(provider.getAuthHeader()).toBe('Bearer stable-token');
    expect(provider.getAuthHeader()).toBe('Bearer stable-token');
  });
});
