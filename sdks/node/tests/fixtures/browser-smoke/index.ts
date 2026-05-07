import {
  TaskClient,
  TaskSession,
  StreamRef,
  loadBlocksConfig,
  decodeInlineArtifact,
} from '@blocks-network/sdk';

// Verify imports resolve — this file is bundled, not executed.
console.log(TaskClient, TaskSession, StreamRef);
console.log(loadBlocksConfig, decodeInlineArtifact);
