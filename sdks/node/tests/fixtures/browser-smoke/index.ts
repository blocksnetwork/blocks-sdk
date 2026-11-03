import {
  TaskClient,
  TaskSession,
  StreamRef,
  loadBlocksConfig,
  decodeInlineArtifact,
  BlocksClient,
} from '@blocks-network/sdk';

// Verify imports resolve — this file is bundled, not executed.
console.log(TaskClient, TaskSession, StreamRef, BlocksClient);
console.log(loadBlocksConfig, decodeInlineArtifact);
