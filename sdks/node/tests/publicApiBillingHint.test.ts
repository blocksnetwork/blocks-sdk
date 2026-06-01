// Lock the package-root re-exports cited by the BillingModeMismatch hint.
//
// The backend hint tells users to call
// `(await getAgent(name)).billingMode` to recover from a
// BillingModeMismatch. That recipe assumes both `getAgent` and the
// `AgentEntry` type are reachable from the package root. This test fails
// fast if either is renamed, dropped, or moved back behind a submodule
// path so the shipped hint can't drift from the public surface.

import { describe, it, expectTypeOf } from 'vitest';
import * as blocksSdk from '../src/index.js';
import type { AgentEntry } from '../src/index.js';
import { getAgent as registryGetAgent } from '../src/runtime/agent-registry.js';

describe('public API surface — BillingModeMismatch hint helpers', () => {
  it('re-exports getAgent from the package root pointing at the registry implementation', () => {
    expectTypeOf(blocksSdk.getAgent).toEqualTypeOf<typeof registryGetAgent>();
  });

  it('re-exports the AgentEntry type from the package root', () => {
    expectTypeOf<AgentEntry>().toHaveProperty('billingMode');
  });
});
