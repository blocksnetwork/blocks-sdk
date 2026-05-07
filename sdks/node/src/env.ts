/**
 * Read an environment variable, returning undefined in non-Node
 * environments (browsers, edge runtimes) where process is unavailable.
 */
export function getEnv(key: string): string | undefined {
  if (typeof process !== 'undefined' && process.env) {
    return process.env[key];
  }
  return undefined;
}
