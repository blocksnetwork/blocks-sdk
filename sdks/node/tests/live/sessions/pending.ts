import { it, type TestFunction } from 'vitest';

// Removal rule: fixtures/sessions/README.md, "Rule for implementing changes".
const RUN_PENDING = process.env.SESSIONS_RUN_PENDING === '1';

export function pendingUntil(feature: string): typeof it {
  return Object.assign(
    (name: string, fn?: TestFunction, timeout?: number) =>
      (RUN_PENDING ? it : it.skip)(`${name} [pending ${feature}]`, fn, timeout),
    it,
  ) as typeof it;
}
