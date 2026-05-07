import { getEnv } from '../env.js';

export const DEFAULT_CDM_URL = 'https://config.blocks.ai/config.json';

export interface CdmKeyset {
  publishKey: string;
  subscribeKey: string;
}

export interface CdmApiConfig {
  baseUrl: string;
  clientId?: string;
}

export interface CdmConfig {
  playground: CdmKeyset;
  network: CdmKeyset;
  api: CdmApiConfig;
}

export type PnEnvironment = 'playground' | 'network';

export async function fetchCdmConfig(url?: string): Promise<CdmConfig> {
  const cdmUrl = url ?? getEnv('BLOCKS_CDM_URL') ?? DEFAULT_CDM_URL;

  const response = await fetch(cdmUrl);
  if (!response.ok) {
    throw new Error(`CDM config fetch failed: ${response.status} ${response.statusText}`);
  }
  const config = (await response.json()) as CdmConfig;

  if (!config.playground?.publishKey || !config.playground?.subscribeKey) {
    throw new Error('CDM config missing playground keys');
  }
  if (!config.network?.publishKey || !config.network?.subscribeKey) {
    throw new Error('CDM config missing network keys');
  }

  return config;
}
