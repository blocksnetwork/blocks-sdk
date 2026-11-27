import { expectTypeOf, test } from 'vitest';
import type { PolicyAppliedEvent, PolicyDenial, TaskEvent } from '../src/index.js';
import { policyDenialFromTerminal } from '../src/index.js';

test('the policy types a consumer imports from the package root', () => {
  expectTypeOf<PolicyAppliedEvent['type']>().toEqualTypeOf<'policy_applied'>();
  expectTypeOf<PolicyAppliedEvent['policyIds']>().toEqualTypeOf<string[]>();
  expectTypeOf<PolicyAppliedEvent>().toMatchTypeOf<TaskEvent>();
  expectTypeOf<PolicyDenial['code']>().toEqualTypeOf<'policy_rejected' | 'policy_unavailable'>();
  expectTypeOf(policyDenialFromTerminal).returns.toEqualTypeOf<PolicyDenial | null>();
});
