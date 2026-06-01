/**
 * Boot-time guard for the silent-park fix.
 *
 * The PubNub JS SDK's makeConfiguration() invokes
 * RequestRetryPolicy.validate() on the policy we hand it. The default
 * ExponentialRetryPolicy.validate() throws when maximumRetry > 6 — even
 * though the docs and the runtime shouldRetry() logic have no such cap.
 *
 * This test uses the *real* PubNub module (no module-level mock) to make
 * sure createPubNubClient with the default subscribeRetryUnbounded:true
 * never throws at construction. Without the validate() override, the
 * agent boot crashes with "Maximum retry for exponential retry policy
 * can not be more than 6".
 */
import { describe, it, expect } from 'vitest';
import { createPubNubClient } from '../src/runtime/pubnub-client.js';

describe('createPubNubClient with real pubnub module', () => {
  it('boots without throwing when subscribeRetryUnbounded is on (default)', () => {
    expect(() => {
      const pn = createPubNubClient({
        publishKey: 'pub-test',
        subscribeKey: 'sub-test',
        userId: 'real-retry-test',
      });
      // Cleanly tear down so we don't leak the heartbeat loop.
      pn.destroy();
    }).not.toThrow();
  });
});
