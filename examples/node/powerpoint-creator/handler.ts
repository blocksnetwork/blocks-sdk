import { spawn } from 'node:child_process';
import { promises as fs } from 'node:fs';
import { tmpdir } from 'node:os';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * PowerPoint Creation Handler.
 *
 * Creates PPTX presentations from structured content using
 * the python-pptx based script (generate_ppt.py).
 *
 * Requires Python 3 with python-pptx installed.
 *
 * Input format:
 *   {
 *     kind: "presentation",
 *     title: "My Presentation",
 *     subtitle: "Optional subtitle",
 *     slides: [
 *       { title: "Slide 1", bullets: ["Point 1", "Point 2"] },
 *       { title: "Slide 2", content: "Full text content" }
 *     ]
 *   }
 *
 * Output: PPTX file artifact
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = extractPresentationInput(task);

  if (!input) {
    const artifact = {
      ok: false,
      error: 'Missing presentation input with title and slides',
      example: {
        kind: 'presentation',
        title: 'My Presentation',
        subtitle: 'A demo presentation',
        slides: [
          { title: 'Introduction', bullets: ['Point 1', 'Point 2', 'Point 3'] },
          { title: 'Details', content: 'Full paragraph content here...' },
        ],
        theme: 'light',
      },
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }

  console.log(`[PowerPointCreator] Creating PPTX: ${input.title}`);
  console.log(`[PowerPointCreator] Slides: ${input.slides.length}`);
  ctx?.reportStatus(`Creating presentation: "${input.title}" (${input.slides.length} slides)`);

  try {
    const pptx = await generatePptx(input);
    console.log(`[PowerPointCreator] Generated PPTX (${pptx.length} bytes)`);

    const sanitizedTitle = sanitizeForFilename(input.title);
    const shortTaskId = task.taskId.slice(0, 8);
    const fileName = `${sanitizedTitle}_${shortTaskId}.pptx`;

    return { artifact: pptx, mimeType: PPTX_MIME, fileName };
  } catch (err) {
    const artifact = {
      ok: false,
      error: (err as Error).message,
      input: input.title,
      hint: 'Ensure Python 3 and python-pptx are installed. You can set PYTHON_BIN to the Python executable.',
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }
}

// ---------------------------------------------------------------------------
// PPTX generation via Python script
// ---------------------------------------------------------------------------

const PPTX_MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation';

// Resolve the Python script relative to this file.
const scriptPath = fileURLToPath(
  new URL('./generate_ppt.py', import.meta.url),
);

async function generatePptx(input: PresentationInput): Promise<Buffer> {
  const tmpBase = path.join(
    tmpdir(),
    `blocks-ppt-${Date.now()}-${Math.random().toString(16).slice(2)}`,
  );
  const inputPath = `${tmpBase}.json`;
  const outputPath = `${tmpBase}.pptx`;

  try {
    await fs.writeFile(inputPath, JSON.stringify(input, null, 2), 'utf8');
    await runPython([scriptPath, '--input', inputPath, '--output', outputPath]);
    return await fs.readFile(outputPath);
  } finally {
    await Promise.allSettled([fs.unlink(inputPath), fs.unlink(outputPath)]);
  }
}

// ---------------------------------------------------------------------------
// Python runner
// ---------------------------------------------------------------------------

function getPythonCandidates(): string[] {
  const candidates: string[] = [];
  const envPython = process.env.PYTHON_BIN || process.env.PYTHON;
  if (envPython) candidates.push(envPython);
  candidates.push('python3', 'python');
  return Array.from(new Set(candidates));
}

function runCommand(
  command: string,
  args: string[],
): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';

    child.stdout.on('data', (data: Buffer) => { stdout += data.toString(); });
    child.stderr.on('data', (data: Buffer) => { stderr += data.toString(); });
    child.on('error', (err) => { reject(err); });
    child.on('close', (code) => {
      if (code === 0) {
        resolve({ stdout, stderr });
      } else {
        const tail = stderr.trim().slice(-500);
        reject(new Error(`generate_ppt.py failed (exit ${code ?? 'unknown'}). ${tail || 'No stderr output.'}`));
      }
    });
  });
}

async function runPython(args: string[]): Promise<void> {
  let lastError: unknown;
  for (const candidate of getPythonCandidates()) {
    try {
      await runCommand(candidate, args);
      return;
    } catch (err) {
      if ((err as NodeJS.ErrnoException)?.code === 'ENOENT') {
        lastError = err;
        continue;
      }
      throw err;
    }
  }
  throw new Error(
    lastError instanceof Error
      ? lastError.message
      : 'Python not found in PATH. Set PYTHON_BIN or PYTHON.',
  );
}

// ---------------------------------------------------------------------------
// Types and input parsing
// ---------------------------------------------------------------------------

interface SlideContent {
  title: string;
  bullets?: string[];
  content?: string;
  notes?: string;
  layout?: 'title' | 'bullets' | 'content' | 'two-column';
}

interface PresentationInput {
  kind?: string;
  title: string;
  subtitle?: string;
  author?: string;
  slides: SlideContent[];
  theme?: 'dark' | 'light' | 'corporate';
}

function isPresentationInput(p: unknown): p is PresentationInput {
  return (
    p !== null &&
    typeof p === 'object' &&
    'title' in p &&
    'slides' in p &&
    Array.isArray((p as PresentationInput).slides)
  );
}

function extractPresentationInput(task: StartTaskMessage): PresentationInput | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (isPresentationInput(p)) return p;
    // Also check for nested structure
    if (p && typeof p === 'object' && 'presentation' in p) {
      const nested = (p as { presentation: unknown }).presentation;
      if (isPresentationInput(nested)) return nested;
    }
  }
  return undefined;
}

function sanitizeForFilename(str: string): string {
  return str
    .replace(/[^a-zA-Z0-9\s-]/g, '')
    .replace(/\s+/g, '_')
    .replace(/_+/g, '_')
    .slice(0, 50);
}
