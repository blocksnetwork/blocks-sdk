import 'dotenv/config';
import { createPubNubClient, fetchCdmConfig, TaskClient } from '@blocks-network/sdk';
import type { TaskEvent } from '@blocks-network/sdk';

/**
 * Trigger a task on the request_nostream_node agent and print the result.
 * Usage: npx tsx trigger.ts
 */
async function main() {
  const cdmConfig = await fetchCdmConfig();
  const { publishKey, subscribeKey } = cdmConfig.playground;
  const baseUrl = cdmConfig.api.baseUrl;
  const authToken = process.env.BLOCKS_TOKEN || undefined;

  // Derive userId from the BLOCKS_TOKEN JWT (sub claim)
  const userId = authToken
    ? JSON.parse(Buffer.from(authToken.split('.')[1], 'base64').toString()).sub
    : 'trigger-script';

  const client = new TaskClient({
    subscribeKey,
    authToken,
    baseUrl,
    createPubNub: () =>
      createPubNubClient({ subscribeKey, publishKey, userId }),
  });

  const session = await client.sendMessage({
    agentName: 'request_nostream_node',
    ownerId: userId,
    requestParts: [
      {
        partId: 'request',
        text: JSON.stringify({ text: 'Hello from trigger!' }),
      },
    ],
  });

  console.log('Task created:', session.taskId);

  session.onProgress((event: TaskEvent) => {
    console.log('[progress]', event.message ?? event.progress ?? '');
  });
  session.onArtifact((event: TaskEvent) => {
    const ref = (event as Record<string, unknown>).artifactRef as
      | Record<string, unknown>
      | undefined;
    if (ref?.kind === 'inline' && typeof ref.data === 'string') {
      console.log('[artifact]', Buffer.from(ref.data, 'base64').toString());
    } else if (ref?.fileUrl && typeof ref.fileUrl === 'string') {
      fetch(ref.fileUrl)
        .then((r) => r.text())
        .then((text) => console.log('[artifact]', text))
        .catch((err) => console.error('[artifact] download failed:', err));
    } else {
      console.log('[artifact]', JSON.stringify(ref));
    }
  });
  session.onTerminal((_event: TaskEvent) => {
    console.log('[done] Task complete');
    session.close();
    client.destroy();
    process.exit(0);
  });
}

main().catch(console.error);
