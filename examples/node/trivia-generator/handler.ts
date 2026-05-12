import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Trivia Generator Handler using OpenAI.
 *
 * Generates trivia questions on any topic with configurable difficulty.
 * Requires OPENAI_API_KEY environment variable.
 *
 * Input format:
 *   { kind: "trivia", topic: "Space exploration", difficulty: "medium", questionCount: 5 }
 *
 * Output: JSON with questions, options, answers, and optional hints
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = extractTriviaInput(task);

  if (!input) {
    const artifact = {
      ok: false,
      error: 'Missing trivia input with topic field',
      example: {
        kind: 'trivia',
        topic: 'Ancient Rome',
        difficulty: 'medium',
        questionCount: 5,
        includeHints: true,
      },
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }

  const questionCount = Math.min(Math.max(input.questionCount || 5, 1), 10);

  console.log(`[TriviaGenerator] Generating trivia for topic: ${input.topic}`);
  console.log(`[TriviaGenerator] Difficulty: ${input.difficulty || 'medium'}, Count: ${questionCount}`);
  ctx?.reportStatus(`Generating ${questionCount} trivia questions about "${input.topic}"...`);

  try {
    const trivia = await generateTrivia(input);
    console.log(`[TriviaGenerator] Generated ${trivia.questions.length} questions`);
    return { artifact: JSON.stringify(trivia, null, 2), mimeType: 'application/json' };
  } catch (err) {
    const artifact = {
      ok: false,
      error: (err as Error).message,
      topic: input.topic,
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }
}

// ---------------------------------------------------------------------------
// OpenAI API client
// ---------------------------------------------------------------------------

async function generateTrivia(input: TriviaInput): Promise<TriviaOutput> {
  const apiKey = process.env.OPENAI_API_KEY;
  if (!apiKey) {
    throw new Error('Missing OPENAI_API_KEY environment variable.');
  }

  const difficulty = input.difficulty || 'medium';
  const questionCount = Math.min(Math.max(input.questionCount || 5, 1), 10);
  const includeHints = input.includeHints ?? true;

  const difficultyDescriptions: Record<string, string> = {
    easy: 'suitable for casual players, common knowledge',
    medium: 'moderately challenging, requires some specific knowledge',
    hard: 'very challenging, requires expert-level knowledge',
  };

  const prompt = `Generate ${questionCount} trivia questions about "${input.topic}".

Difficulty level: ${difficulty} (${difficultyDescriptions[difficulty]})

For each question, provide:
- A clear, unambiguous question
- Exactly 4 multiple choice options (A, B, C, D)
- The correct answer
${includeHints ? '- A helpful hint that does not give away the answer' : ''}
- A fun fact related to the question

Return ONLY valid JSON in this exact format:
{
  "questions": [
    {
      "question": "What is...?",
      "options": ["A) First option", "B) Second option", "C) Third option", "D) Fourth option"],
      "correctAnswer": "A) First option",
      ${includeHints ? '"hint": "Think about...",' : ''}
      "funFact": "Did you know that..."
    }
  ]
}`;

  const response = await fetch('https://api.openai.com/v1/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${apiKey}`,
    },
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      messages: [
        {
          role: 'system',
          content: 'You are a trivia expert who creates engaging, accurate, and fun trivia questions. Always return valid JSON.',
        },
        { role: 'user', content: prompt },
      ],
      temperature: 0.8,
      max_tokens: 2000,
    }),
  });

  if (!response.ok) {
    const errorText = await response.text();
    throw new Error(`OpenAI API error: ${response.status} - ${errorText}`);
  }

  const result = await response.json();
  const content = result.choices?.[0]?.message?.content;

  if (!content) {
    throw new Error('No content returned from OpenAI API.');
  }

  // Parse JSON from response, handling potential markdown code blocks
  let jsonContent = content.trim();
  if (jsonContent.startsWith('```')) {
    jsonContent = jsonContent.replace(/^```(?:json)?\n?/, '').replace(/\n?```$/, '');
  }

  const parsed = JSON.parse(jsonContent);

  return {
    ok: true,
    topic: input.topic,
    difficulty,
    questions: parsed.questions,
  };
}

// ---------------------------------------------------------------------------
// Types and input parsing
// ---------------------------------------------------------------------------

interface TriviaInput {
  kind?: string;
  topic: string;
  difficulty?: 'easy' | 'medium' | 'hard';
  questionCount?: number;
  includeHints?: boolean;
}

interface TriviaQuestion {
  question: string;
  options: string[];
  correctAnswer: string;
  hint?: string;
  funFact?: string;
}

interface TriviaOutput {
  ok: true;
  topic: string;
  difficulty: string;
  questions: TriviaQuestion[];
}

function extractTriviaInput(task: StartTaskMessage): TriviaInput | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (p !== null && typeof p === 'object' && 'topic' in p) {
      return p as TriviaInput;
    }
  }
  return undefined;
}
