import typescript from '@rollup/plugin-typescript';
import replace from '@rollup/plugin-replace';
import nodeResolve from '@rollup/plugin-node-resolve';
import commonjs from '@rollup/plugin-commonjs';
import json from '@rollup/plugin-json';
import terser from '@rollup/plugin-terser';
import dts from 'rollup-plugin-dts';

// Build-time injection of the default Blocks backend base URL.
// Override with `BLOCKS_BACKEND_BASE_URL=...` for on-prem / staging builds.
const BACKEND_BASE_URL_DEFAULT =
  process.env.BLOCKS_BACKEND_BASE_URL || 'https://app.blocks.ai';

const replacePlugin = () =>
  replace({
    preventAssignment: true,
    values: {
      __BACKEND_BASE_URL_DEFAULT__: JSON.stringify(BACKEND_BASE_URL_DEFAULT),
    },
  });

const tsPlugin = (overrides = {}) =>
  typescript({
    tsconfig: './tsconfig.json',
    compilerOptions: {
      declaration: false,
      emitDeclarationOnly: false,
      ...overrides,
    },
  });

export default [
  // IIFE bundle — script-tag consumers. Bundles the SDK directly.
  {
    input: 'src/index.ts',
    external: [],
    plugins: [
      replacePlugin(),
      json(),
      nodeResolve({ browser: true, preferBuiltins: false }),
      commonjs(),
      tsPlugin(),
      terser(),
    ],
    output: {
      file: 'dist/blocks-auth.iife.min.js',
      format: 'iife',
      name: 'BlocksAuth',
      sourcemap: false,
    },
  },
  // ESM bundle — bundler consumers; SDK is external (peer dep).
  {
    input: 'src/index.ts',
    external: ['@blocks-network/sdk'],
    plugins: [
      replacePlugin(),
      json(),
      nodeResolve({ browser: true, preferBuiltins: false }),
      commonjs(),
      tsPlugin(),
    ],
    output: {
      file: 'dist/index.mjs',
      format: 'es',
      sourcemap: false,
    },
  },
  // CJS bundle — Node fallback consumers; SDK is external (peer dep).
  {
    input: 'src/index.ts',
    external: ['@blocks-network/sdk'],
    plugins: [
      replacePlugin(),
      json(),
      nodeResolve({ browser: true, preferBuiltins: false }),
      commonjs(),
      tsPlugin(),
    ],
    output: {
      file: 'dist/index.cjs',
      format: 'cjs',
      exports: 'named',
      sourcemap: false,
    },
  },
  // Type declarations bundle.
  {
    input: 'src/index.ts',
    external: ['@blocks-network/sdk'],
    plugins: [json(), dts()],
    output: {
      file: 'dist/index.d.ts',
      format: 'es',
    },
  },
];
