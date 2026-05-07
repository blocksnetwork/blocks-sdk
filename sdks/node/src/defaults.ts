import { DEFAULT_CDM_URL } from './runtime/cdm-config.js';

export const DEFAULTS = {
  cdmUrl: DEFAULT_CDM_URL,
  inlineLimitBytes: 16_384,
  maxUploadBytes: 26_214_400, // 25 MB — must match afui_mvp_backend MAX_FILE_SIZE_BYTES
  concurrency: 1,
  expectedInstances: 1,
};
