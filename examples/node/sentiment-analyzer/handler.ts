import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Sentiment Analysis Handler
 *
 * Analyzes text sentiment using a rule-based approach.
 * Returns sentiment scores, keywords, and classification.
 *
 * Input format:
 * { kind: 'text_analysis', text: 'Your text to analyze...' }
 * or simply: { text: 'Your text to analyze...' }
 *
 * Output: JSON with sentiment analysis results
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = extractTextInput(task);

  if (!input) {
    const artifact = {
      ok: false,
      error: 'Missing text input for sentiment analysis',
      example: {
        kind: 'text_analysis',
        text: 'I really love this product! It is absolutely amazing.',
      },
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }

  console.log(`[SentimentAnalyzer] Analyzing text (${input.text.length} chars)`);
  ctx?.reportStatus('Analyzing sentiment');

  try {
    const result = analyzeSentiment(input.text);
    console.log(`[SentimentAnalyzer] Result: ${result.sentiment} (confidence: ${result.confidence.toFixed(2)})`);

    const output = {
      ok: true,
      taskId: task.taskId,
      input: {
        textLength: input.text.length,
        language: input.language,
        preview: input.text.slice(0, 100) + (input.text.length > 100 ? '...' : ''),
      },
      analysis: result,
      analyzedAt: new Date().toISOString(),
    };

    return { artifact: JSON.stringify(output, null, 2), mimeType: 'application/json' };
  } catch (err) {
    const artifact = { ok: false, error: (err as Error).message };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }
}

// ---------------------------------------------------------------------------
// Input extraction
// ---------------------------------------------------------------------------

interface TextAnalysisPart {
  kind?: string;
  text?: string;
  language?: string;
}

function extractTextInput(task: StartTaskMessage): { text: string; language: string } | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (p === null || typeof p !== 'object') continue;
    const part = p as TextAnalysisPart;
    if (typeof part.text === 'string' && part.text.length > 0) {
      return { text: part.text, language: part.language ?? 'en' };
    }
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Sentiment word lists
// ---------------------------------------------------------------------------

const POSITIVE_WORDS = new Set([
  'good', 'great', 'excellent', 'amazing', 'wonderful', 'fantastic', 'awesome',
  'love', 'happy', 'joy', 'pleased', 'delighted', 'satisfied', 'perfect',
  'best', 'brilliant', 'outstanding', 'superb', 'terrific', 'marvelous',
  'positive', 'beautiful', 'helpful', 'friendly', 'nice', 'pleasant',
  'impressive', 'remarkable', 'exceptional', 'incredible', 'successful',
]);

const NEGATIVE_WORDS = new Set([
  'bad', 'terrible', 'awful', 'horrible', 'poor', 'worst', 'hate',
  'sad', 'angry', 'disappointed', 'frustrated', 'annoyed', 'upset',
  'negative', 'ugly', 'unhelpful', 'unfriendly', 'unpleasant', 'rude',
  'fail', 'failure', 'problem', 'issue', 'broken', 'wrong', 'error',
  'difficult', 'hard', 'confusing', 'complicated', 'impossible',
]);

const INTENSIFIERS = new Set([
  'very', 'really', 'extremely', 'incredibly', 'absolutely', 'totally',
  'completely', 'utterly', 'highly', 'super', 'so', 'quite',
]);

const NEGATORS = new Set([
  'not', 'no', "don't", "doesn't", "didn't", "won't", "wouldn't",
  "can't", "cannot", "never", "neither", "nor", "none",
]);

// ---------------------------------------------------------------------------
// Analysis
// ---------------------------------------------------------------------------

interface SentimentResult {
  sentiment: 'positive' | 'negative' | 'neutral' | 'mixed';
  confidence: number;
  scores: { positive: number; negative: number; neutral: number };
  keywords: { positive: string[]; negative: string[] };
  statistics: { wordCount: number; sentenceCount: number; avgSentenceLength: number };
  details: {
    intensifiedPositive: number;
    intensifiedNegative: number;
    negatedPositive: number;
    negatedNegative: number;
  };
}

function analyzeSentiment(text: string): SentimentResult {
  const words = text.toLowerCase().split(/\s+/).filter(w => w.length > 0);
  const sentences = text.split(/[.!?]+/).filter(s => s.trim().length > 0);

  let positiveCount = 0;
  let negativeCount = 0;
  let intensifiedPositive = 0;
  let intensifiedNegative = 0;
  let negatedPositive = 0;
  let negatedNegative = 0;

  const positiveKeywords: string[] = [];
  const negativeKeywords: string[] = [];

  for (let i = 0; i < words.length; i++) {
    const word = words[i].replace(/[^a-z]/g, '');
    const prevWord = i > 0 ? words[i - 1].replace(/[^a-z]/g, '') : '';
    const prevPrevWord = i > 1 ? words[i - 2].replace(/[^a-z]/g, '') : '';

    const hasIntensifier = INTENSIFIERS.has(prevWord);
    const hasNegator = NEGATORS.has(prevWord) || NEGATORS.has(prevPrevWord);

    if (POSITIVE_WORDS.has(word)) {
      if (hasNegator) {
        negatedPositive++;
        negativeCount += 0.5;
      } else {
        const weight = hasIntensifier ? 1.5 : 1;
        positiveCount += weight;
        if (hasIntensifier) intensifiedPositive++;
        if (!positiveKeywords.includes(word)) positiveKeywords.push(word);
      }
    } else if (NEGATIVE_WORDS.has(word)) {
      if (hasNegator) {
        negatedNegative++;
        positiveCount += 0.5;
      } else {
        const weight = hasIntensifier ? 1.5 : 1;
        negativeCount += weight;
        if (hasIntensifier) intensifiedNegative++;
        if (!negativeKeywords.includes(word)) negativeKeywords.push(word);
      }
    }
  }

  const totalSentimentWords = positiveCount + negativeCount || 1;
  const positiveScore = positiveCount / totalSentimentWords;
  const negativeScore = negativeCount / totalSentimentWords;
  const neutralScore = 1 - Math.min(1, (positiveCount + negativeCount) / Math.max(words.length * 0.1, 1));

  let sentiment: SentimentResult['sentiment'];
  let confidence: number;

  const scoreDiff = Math.abs(positiveScore - negativeScore);

  if (positiveScore < 0.1 && negativeScore < 0.1) {
    sentiment = 'neutral';
    confidence = 0.7 + neutralScore * 0.3;
  } else if (scoreDiff < 0.2 && positiveScore > 0.3 && negativeScore > 0.3) {
    sentiment = 'mixed';
    confidence = 0.5 + scoreDiff;
  } else if (positiveScore > negativeScore) {
    sentiment = 'positive';
    confidence = 0.5 + scoreDiff * 0.5;
  } else {
    sentiment = 'negative';
    confidence = 0.5 + scoreDiff * 0.5;
  }

  return {
    sentiment,
    confidence: Math.min(1, confidence),
    scores: {
      positive: Math.round(positiveScore * 100) / 100,
      negative: Math.round(negativeScore * 100) / 100,
      neutral: Math.round(neutralScore * 100) / 100,
    },
    keywords: {
      positive: positiveKeywords.slice(0, 10),
      negative: negativeKeywords.slice(0, 10),
    },
    statistics: {
      wordCount: words.length,
      sentenceCount: sentences.length,
      avgSentenceLength: Math.round(words.length / Math.max(sentences.length, 1)),
    },
    details: {
      intensifiedPositive,
      intensifiedNegative,
      negatedPositive,
      negatedNegative,
    },
  };
}
