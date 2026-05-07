import PubNub from 'pubnub';

export interface PubNubClientConfig {
  subscribeKey: string;
  publishKey: string;
  userId?: string;
  presenceTimeout?: number;
}

export const createPubNubClient = (config: PubNubClientConfig) => {
  if (!config.subscribeKey || !config.publishKey) {
    throw new Error('PUBNUB keys not configured');
  }
  return new PubNub({
    publishKey: config.publishKey,
    subscribeKey: config.subscribeKey,
    userId: config.userId ?? 'blocks-agent',
    enableEventEngine: true,
    ...(config.presenceTimeout !== undefined ? { presenceTimeout: config.presenceTimeout } : {}),
  });
};
