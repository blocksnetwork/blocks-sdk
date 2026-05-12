import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Image Generator Handler using Google Gemini API.
 *
 * Generates images from text prompts.
 * Requires GEMINI_API_KEY or GOOGLE_API_KEY environment variable.
 *
 * Input format:
 *   { kind: "image_prompt", prompt: "A cozy reading nook..." }
 *
 * Output: PNG image as artifact
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = extractImagePrompt(task);

  if (!input) {
    const artifact = {
      ok: false,
      error: 'Missing image_prompt with prompt field',
      example: { kind: 'image_prompt', prompt: 'A futuristic city at sunset' },
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }

  const promptPreview =
    input.prompt.length > 50 ? input.prompt.slice(0, 50) + '...' : input.prompt;

  console.log(`[ImageGenerator] Generating image for prompt: ${input.prompt.slice(0, 100)}...`);
  ctx?.reportStatus(`Generating image: "${promptPreview}"`);

  try {
    const { data, mimeType } = await generateImage(input.prompt, input.model);
    console.log(`[ImageGenerator] Generated image: ${data.length} bytes, ${mimeType}`);
    return { artifact: data, mimeType };
  } catch (err) {
    const artifact = {
      ok: false,
      error: (err as Error).message,
      prompt: input.prompt,
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }
}

// ---------------------------------------------------------------------------
// Gemini API client
// ---------------------------------------------------------------------------

async function generateImage(
  prompt: string,
  model: string,
): Promise<{ data: Buffer; mimeType: string }> {
  const apiKey = process.env.GOOGLE_API_KEY || process.env.GEMINI_API_KEY;
  if (!apiKey) {
    throw new Error(
      'Missing API key. Set GEMINI_API_KEY or GOOGLE_API_KEY environment variable.',
    );
  }

  const endpoint = `https://generativelanguage.googleapis.com/v1beta/models/${model}:generateContent?key=${apiKey}`;

  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      contents: [{ parts: [{ text: prompt }] }],
      generationConfig: { responseModalities: ['IMAGE'] },
    }),
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`Gemini API error: ${response.status} - ${errorText}`);
  }

  const result = await response.json();

  // Extract image from response
  const candidates = result.candidates ?? [];
  for (const candidate of candidates) {
    const parts = candidate.content?.parts ?? [];
    for (const part of parts) {
      // Support both camelCase and snake_case response shapes
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const inlineData = (part as any).inlineData ?? (part as any).inline_data;
      if (inlineData?.data) {
        const mime = inlineData.mimeType ?? inlineData.mime_type ?? 'image/png';
        return { data: Buffer.from(inlineData.data, 'base64'), mimeType: mime };
      }
    }
  }

  throw new Error('No image returned from Gemini API. Try a more detailed prompt.');
}

// ---------------------------------------------------------------------------
// Input parsing
// ---------------------------------------------------------------------------

interface ImagePromptPart {
  kind?: string;
  prompt?: string;
  text?: string;
  model?: string;
}

function extractImagePrompt(
  task: StartTaskMessage,
): { prompt: string; model: string } | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (p === null || typeof p !== 'object') continue;
    const part = p as ImagePromptPart;
    const prompt = part.prompt ?? part.text;
    if (typeof prompt === 'string' && prompt.length > 0) {
      return { prompt, model: part.model ?? 'gemini-3-pro-image-preview' };
    }
  }
  return undefined;
}
