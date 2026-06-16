/**
 * Runtime envelope validation. Compiles the two postMessage envelope JSON
 * schemas (success + error) once at module-load time via Ajv 2020-12 +
 * ajv-formats so the popup listener can `validateSuccessEnvelope` /
 * `validateErrorEnvelope` an inbound `event.data` in O(n).
 *
 * The schemas are imported as JSON modules (TS `resolveJsonModule: true`),
 * which inlines them into the bundle — no runtime fs read, no fetch.
 */

import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';

// Schemas are checked-in copies under `src/__schemas__/`. The originals live
// at `schemas/embedded-auth/`; `test/schemas.parity.test.ts` enforces byte-
// identity. Keeping the imports inside `src/` keeps the TS rootDir happy and
// lets `@rollup/plugin-json` inline them at bundle time without escaping the
// package boundary.
import successSchema from './__schemas__/postmessage-envelope.success.schema.json';
import errorSchema from './__schemas__/postmessage-envelope.error.schema.json';

import type {
  BlocksAuthSuccessEnvelope,
  BlocksAuthErrorEnvelope,
} from './types.js';

// Ajv 8 ESM/CJS interop quirk: the default export is sometimes wrapped
// under `default` when reached through the Node CJS bridge. The cast to
// `any` also sidesteps duplicate-package type-identity noise that crops
// up when two copies of the `ajv` types live in the workspace tree.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const Ajv2020Any: any = (Ajv2020 as any).default ?? Ajv2020;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const addFormatsAny: any = (addFormats as any).default ?? addFormats;

const ajv = new Ajv2020Any({ strict: false, allErrors: false });
addFormatsAny(ajv);

const compiledSuccess = ajv.compile(successSchema);
const compiledError = ajv.compile(errorSchema);

export const validateSuccessEnvelope = (
  msg: unknown,
): msg is BlocksAuthSuccessEnvelope => compiledSuccess(msg) === true;

export const validateErrorEnvelope = (
  msg: unknown,
): msg is BlocksAuthErrorEnvelope => compiledError(msg) === true;
