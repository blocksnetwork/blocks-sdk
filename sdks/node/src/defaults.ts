import { DEFAULT_CDM_URL } from './runtime/cdm-config.js';

export const DEFAULTS = {
  cdmUrl: DEFAULT_CDM_URL,
  inlineLimitBytes: 16_384,
  maxUploadBytes: 1_073_741_824, // platform upload limit; the service rejects larger files
  concurrency: 1,
  expectedInstances: 1,
};
