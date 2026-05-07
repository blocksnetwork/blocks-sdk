/**
 * Fetch Blocks configuration from a CDN-hosted JSON file.
 * Browser-safe. Returns PubNub keys and backend URL.
 */
export interface BlocksConfig {
  publishKey: string;
  subscribeKey: string;
  blocksBackendUrl: string;
}

export async function loadBlocksConfig(url: string): Promise<BlocksConfig> {
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`Failed to load Blocks config from ${url}: ${res.status}`);
  }
  const json = await res.json() as Record<string, unknown>;
  if (!json.subscribeKey) {
    throw new Error('Invalid Blocks config: missing subscribeKey');
  }
  return {
    publishKey: (json.publishKey as string) ?? '',
    subscribeKey: json.subscribeKey as string,
    blocksBackendUrl: (json.blocksBackendUrl as string) ?? '',
  };
}
