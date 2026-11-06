import { createHash } from 'node:crypto';
import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext is required');

  const parts = task.requestParts ?? [];
  const filePart = parts.find((part) => part.partId === 'input_file');
  const fileRef = filePart?.artifactRef;
  if (!filePart || !fileRef) throw new Error('Missing required file input "input_file"');
  const note = parts.find((part) => part.partId === 'text')?.text;

  ctx.reportStatus('Downloading the file...');
  const data = await ctx.downloadInputArtifact(filePart);
  const fileName = fileRef.fileName ?? 'file';
  const mimeType = fileRef.mimeType;

  const summary = [
    `file: ${fileName} (${mimeType})`,
    `size: ${data.byteLength} bytes`,
    `sha256: ${createHash('sha256').update(data).digest('hex')}`,
    ...(note ? [`note: ${note}`] : []),
  ].join('\n');

  return {
    artifacts: [
      { data, mimeType, fileName, outputId: 'file' },
      { data: summary, mimeType: 'text/plain', outputId: 'summary' },
    ],
  };
}
