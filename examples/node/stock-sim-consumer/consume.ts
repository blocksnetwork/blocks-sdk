import 'dotenv/config';
import PubNub from 'pubnub';
import { fetchCdmConfig, TaskClient } from '@blocks-network/sdk';
import { promptForStockRequest, runStockSimTask } from './stock-sim-client.js';

const authToken = process.env.BLOCKS_TOKEN;
if (!authToken) {
  console.error('BLOCKS_TOKEN not set. Run "blocks login" first.');
  process.exit(1);
}

const cdmConfig = await fetchCdmConfig();
const { publishKey, subscribeKey } = cdmConfig.playground;
const baseUrl = cdmConfig.api.baseUrl;
const ownerId = `stock-sim-consumer-${Date.now()}`;

const client = new TaskClient({
  subscribeKey,
  publishKey,
  baseUrl,
  authToken,
  createSessionPubNub: () =>
    new PubNub({ subscribeKey, publishKey, userId: ownerId, enableEventEngine: true }),
});

async function main() {
  const request = await promptForStockRequest();
  console.log(
    `Requesting ${request.symbols.join(', ')} from stock-sim for ` +
    `${request.durationMinutes} minute${request.durationMinutes === 1 ? '' : 's'}...`,
  );
  console.log('---');

  const result = await runStockSimTask({
    taskClient: client,
    ownerId,
    request,
    log: (line) => console.log(line),
  });

  console.log('---');
  console.log('Final summary:');
  console.log(JSON.stringify(result, null, 2));
  client.destroy();
}

main().catch((err) => {
  console.error(err);
  client.destroy();
  process.exit(1);
});
