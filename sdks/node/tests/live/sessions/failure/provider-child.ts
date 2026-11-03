// Forked so a test can kill it or cut its network. argv: <agentName> <ProviderMode>.
import { getBaseUrl } from '../../../helpers/live-test-config.js';
import { makeTestCard } from '../../../helpers/test-card.js';
import { echoResult, sleep } from '../session-harness.js';
import { type ExchangeContext, type HandlerResult, loadSessionSdk } from '../session-sdk.js';
import {
  type ChildMessage,
  type ProviderMode,
  TICK_COUNT,
  TICK_INTERVAL_MS,
} from './child-provider.js';
import { goDark, installBlackhole } from './network-blackhole.js';

const [agentName, mode] = process.argv.slice(2) as [string, ProviderMode];

const send = (msg: ChildMessage) => process.send?.(msg);

const handlers: Record<ProviderMode, (exchange: ExchangeContext) => Promise<HandlerResult>> = {
  hang: async (exchange) => {
    await exchange.reportStatus('started');
    return new Promise(() => {});
  },
  ticker: async (exchange) => {
    for (let i = 1; i <= TICK_COUNT; i++) {
      await exchange.reportStatus(`tick-${i}`);
      await sleep(TICK_INTERVAL_MS);
    }
    return echoResult(exchange, 'node');
  },
  zombie: async (exchange) => echoResult(exchange, 'node'),
};

// Cuts every host but the backend: checkpoints continue, session delivery stops.
if (mode === 'zombie') {
  installBlackhole();
  const backendHost = new URL(getBaseUrl()).hostname;
  process.on('message', (msg) => {
    if (msg !== 'go-dark') return;
    goDark((host) => host !== backendHost);
    send({ type: 'dark' });
  });
}

const sdk = await loadSessionSdk();
await sdk.startAgentInstance({
  agentName,
  card: makeTestCard({ agentName }),
  baseUrl: getBaseUrl(),
  onExchange: (_ctx, exchange) => handlers[mode](exchange),
});
send({ type: 'ready' });
