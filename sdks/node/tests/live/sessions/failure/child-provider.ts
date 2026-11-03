import { type ChildProcess, fork } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Exchange, Session, SessionState } from '../session-sdk.js';

export const TICK_COUNT = 8;
// Wider than the once-per-second reportStatus throttle, so no tick is coalesced.
export const TICK_INTERVAL_MS = 1_200;

export type ProviderMode = 'hang' | 'ticker' | 'zombie';

export type ChildMessage = { type: 'ready' } | { type: 'dark' };

const HERE = dirname(fileURLToPath(import.meta.url));
const NODE_SDK_DIR = resolve(HERE, '..', '..', '..', '..');
const CHILD_READY_MS = 30_000;

export function waitForChildMessage(
  child: ChildProcess,
  type: ChildMessage['type'],
): Promise<void> {
  return new Promise((resolvePromise, reject) => {
    const timer = setTimeout(() => reject(new Error(`child sent no ${type}`)), CHILD_READY_MS);
    const onExit = (code: number | null) =>
      reject(new Error(`child exited (${code}) before ${type}`));
    child.once('exit', onExit);
    child.on('message', (msg: ChildMessage) => {
      if (msg.type !== type) return;
      clearTimeout(timer);
      child.off('exit', onExit);
      resolvePromise();
    });
  });
}

export async function spawnProvider(agentName: string, mode: ProviderMode): Promise<ChildProcess> {
  const child = fork(resolve(HERE, 'provider-child.ts'), [agentName, mode], {
    cwd: NODE_SDK_DIR,
    execArgv: ['--import', 'tsx'],
    stdio: 'inherit',
  });
  await waitForChildMessage(child, 'ready');
  return child;
}

export function killChild(child: ChildProcess | undefined): void {
  if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
}

export function recordStates(session: Session): SessionState[] {
  const states: SessionState[] = [];
  session.onStateChange((state) => states.push(state));
  return states;
}

export function firstProgress(exchange: Exchange): Promise<void> {
  return new Promise((r) => exchange.onProgress(() => r()));
}
