import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    // Type-level tests: the only place the package's public types are checked against tests/.
    typecheck: { enabled: true, tsconfig: './tsconfig.typecheck.json', include: ['**/*.test-d.ts'] },
    env: {
      // startAgentInstance() requires BLOCKS_API_KEY since the
      // publish/connect split.  Provide a dummy value so unit tests
      // that mock network calls can proceed without the real key; a
      // live run supplies the real one, which must not be overridden.
      BLOCKS_API_KEY: process.env.BLOCKS_API_KEY ?? 'bk_test-dummy-key',
    },
  },
});
