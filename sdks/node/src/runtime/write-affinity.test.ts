import { describe, it, expect, beforeEach } from 'vitest';
import { captureAffinity, injectAffinity, resetAffinity } from './write-affinity.js';

describe('write-affinity', () => {
  beforeEach(() => {
    resetAffinity();
  });

  it('injects nothing when no affinity has been captured', () => {
    const headers: Record<string, string> = {};
    injectAffinity(headers);
    expect(headers).toEqual({});
  });

  it('captures and injects a valid affinity header', () => {
    const future = String(Math.floor(Date.now() / 1000) + 10);
    captureAffinity(new Headers({ 'x-write-affinity': future }));

    const headers: Record<string, string> = {};
    injectAffinity(headers);
    expect(headers['x-write-affinity']).toBe(future);
  });

  it('does not inject an expired affinity header', () => {
    const past = String(Math.floor(Date.now() / 1000) - 10);
    captureAffinity(new Headers({ 'x-write-affinity': past }));

    const headers: Record<string, string> = {};
    injectAffinity(headers);
    expect(headers).toEqual({});
  });

  it('ignores missing header in response', () => {
    captureAffinity(new Headers());

    const headers: Record<string, string> = {};
    injectAffinity(headers);
    expect(headers).toEqual({});
  });

  it('overwrites older affinity with newer one', () => {
    const older = String(Math.floor(Date.now() / 1000) + 5);
    const newer = String(Math.floor(Date.now() / 1000) + 15);
    captureAffinity(new Headers({ 'x-write-affinity': older }));
    captureAffinity(new Headers({ 'x-write-affinity': newer }));

    const headers: Record<string, string> = {};
    injectAffinity(headers);
    expect(headers['x-write-affinity']).toBe(newer);
  });

  it('capture is monotonic — older arriving after newer is ignored', () => {
    const newer = String(Math.floor(Date.now() / 1000) + 15);
    const older = String(Math.floor(Date.now() / 1000) + 5);
    captureAffinity(new Headers({ 'x-write-affinity': newer }));
    captureAffinity(new Headers({ 'x-write-affinity': older }));

    const headers: Record<string, string> = {};
    injectAffinity(headers);
    expect(headers['x-write-affinity']).toBe(newer);
  });

  it('strips stale header from a reused headers object when state has expired', () => {
    const future = String(Math.floor(Date.now() / 1000) + 10);
    const headers: Record<string, string> = {};
    captureAffinity(new Headers({ 'x-write-affinity': future }));
    injectAffinity(headers);
    expect(headers['x-write-affinity']).toBe(future);

    resetAffinity();
    injectAffinity(headers);
    expect(headers['x-write-affinity']).toBeUndefined();
  });

  it('strips pre-populated header when no state has been captured', () => {
    const headers: Record<string, string> = { 'x-write-affinity': '1234567890' };
    injectAffinity(headers);
    expect(headers['x-write-affinity']).toBeUndefined();
  });

  it.each(['Infinity', '-Infinity', 'NaN', 'not-a-number'])(
    'malformed header value %s does not clobber existing valid state',
    (badValue) => {
      // Number() accepts "Infinity"/"NaN" without throwing — without the
      // Number.isFinite guard, Infinity would pin reads to primary forever.
      const valid = String(Math.floor(Date.now() / 1000) + 10);
      captureAffinity(new Headers({ 'x-write-affinity': valid }));
      captureAffinity(new Headers({ 'x-write-affinity': badValue }));

      const headers: Record<string, string> = {};
      injectAffinity(headers);
      expect(headers['x-write-affinity']).toBe(valid);
    },
  );
});
