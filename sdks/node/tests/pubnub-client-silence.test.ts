import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import PubNub from 'pubnub';
import { buildPubNubLogConfig } from '../src/runtime/pubnub-client.js';

describe('PubNub ephemeral client silence (BLOCKS-374)', () => {
  beforeEach(() => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
  });
  afterEach(() => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    vi.restoreAllMocks();
  });

  it('an ephemeral PubNub built with buildPubNubLogConfig() emits nothing at construction', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    const infoSpy = vi.spyOn(console, 'info').mockImplementation(() => {});

    const pn = new PubNub({
      subscribeKey: 'sub-c-test',
      publishKey: 'pub-c-test',
      userId: 'silence-probe-ephemeral',
      ...buildPubNubLogConfig(),
    });

    expect(logSpy).not.toHaveBeenCalled();
    expect(warnSpy).not.toHaveBeenCalled();
    expect(errSpy).not.toHaveBeenCalled();
    expect(infoSpy).not.toHaveBeenCalled();
    pn.destroy();
  });
});
