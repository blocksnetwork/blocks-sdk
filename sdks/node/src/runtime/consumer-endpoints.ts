import { getEnv } from '../env.js';
import { fetchCdmConfig } from './cdm-config.js';

export type BillingMode = 'free' | 'paid';

export interface ConsumerEndpointOptions {
  billingMode?: BillingMode;
  cdmUrl?: string;
  subscribeKey?: string;
  publishKey?: string;
  baseUrl?: string;
}

export interface ConsumerEndpoints {
  billingMode: BillingMode;
  subscribeKey: string;
  publishKey: string | undefined;
  baseUrl: string;
}

/** Strict equality, not truthiness: any non-'paid' value would otherwise select the free keyset. */
export function assertBillingMode(
  billingMode: unknown,
  caller: string,
): asserts billingMode is BillingMode {
  if (billingMode !== 'free' && billingMode !== 'paid') {
    throw new Error(`${caller} requires a billingMode option ('free' or 'paid')`);
  }
}

/**
 * Resolves the keys and backend URL a consumer client connects with:
 * explicit option, then `BLOCKS_*` env, then the CDM keyset `billingMode` selects.
 */
export async function resolveConsumerEndpoints(
  opts: ConsumerEndpointOptions,
  caller: string,
): Promise<ConsumerEndpoints> {
  const { billingMode } = opts;
  assertBillingMode(billingMode, caller);

  const cdm = await fetchCdmConfig(opts.cdmUrl ?? getEnv('BLOCKS_CDM_URL'));
  const keyset = billingMode === 'paid' ? cdm.network : cdm.playground;

  const subscribeKey = opts.subscribeKey ?? getEnv('BLOCKS_SUBSCRIBE_KEY') ?? keyset.subscribeKey;
  const publishKey = opts.publishKey ?? getEnv('BLOCKS_PUBLISH_KEY') ?? keyset.publishKey;
  const baseUrl = opts.baseUrl ?? getEnv('BLOCKS_BACKEND_URL') ?? cdm.api.baseUrl;

  if (!subscribeKey) {
    throw new Error(`${caller} could not resolve subscribeKey from options, env, or CDM`);
  }
  if (!baseUrl) {
    throw new Error(
      `${caller} could not resolve baseUrl. Set baseUrl option, ` +
        'BLOCKS_BACKEND_URL env var, or ensure CDM config has api.baseUrl.',
    );
  }
  return { billingMode, subscribeKey, publishKey, baseUrl };
}
