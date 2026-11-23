import type { RpcClientConfig } from './rpc-client.js';
import type { TaskClient } from './task-client.js';

/** A header, not a body field: an older backend ignores an unknown header but refuses an unknown body field. */
export const PARENT_TASK_HEADER = 'Blocks-Parent-Task-Id';

/** A view of the agent's one shared client whose calls name `parentTaskId`; all state stays on the shared client. */
export function taskClientForParent(client: TaskClient, parentTaskId: string): TaskClient {
  return new Proxy(client, {
    get(target, prop, receiver) {
      if (prop !== 'config') return Reflect.get(target, prop, receiver);
      return configForParent(Reflect.get(target, prop, target) as RpcClientConfig, parentTaskId);
    },
  });
}

/** Reads `rpcHeaders` with the header added; every other read and every write reaches the shared config. */
function configForParent(config: RpcClientConfig, parentTaskId: string): RpcClientConfig {
  return new Proxy(config, {
    get(target, prop, receiver) {
      if (prop !== 'rpcHeaders') return Reflect.get(target, prop, receiver);
      return { ...target.rpcHeaders, [PARENT_TASK_HEADER]: parentTaskId };
    },
  });
}
