import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Data Transformer Handler
 *
 * Transforms data between formats (CSV to JSON, JSON to CSV)
 * with support for filtering, mapping, and aggregation.
 *
 * Input format:
 *   {
 *     kind: "transform",
 *     data: "name,age\nAlice,30" | [{...}],
 *     inputFormat: "csv" | "json",
 *     outputFormat: "csv" | "json",
 *     options?: { filter, select, rename, aggregate, delimiter, headers }
 *   }
 *
 * Output: Transformed data in the specified format
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = extractTransformInput(task);

  if (!input) {
    const artifact = {
      ok: false,
      error: 'Missing transform input',
      example: {
        kind: 'transform',
        data: 'name,age,city\nAlice,30,NYC\nBob,25,LA',
        inputFormat: 'csv',
        outputFormat: 'json',
        options: { filter: { field: 'age', operator: 'gt', value: 26 } },
      },
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }

  console.log(`[DataTransformer] Transforming ${input.inputFormat} to ${input.outputFormat}`);
  ctx?.reportStatus(`Transforming ${input.inputFormat} → ${input.outputFormat}`);

  try {
    const result = transformData(input);
    console.log(`[DataTransformer] ${result.stats.inputRows} rows → ${result.stats.outputRows} rows`);

    if (input.outputFormat === 'json') {
      const output = {
        ok: true,
        stats: result.stats,
        data: result.data,
        transformedAt: new Date().toISOString(),
      };
      return { artifact: JSON.stringify(output, null, 2), mimeType: 'application/json' };
    }
    return { artifact: result.data as string, mimeType: 'text/csv' };
  } catch (err) {
    const artifact = { ok: false, error: (err as Error).message };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }
}

// ---------------------------------------------------------------------------
// Core transform logic
// ---------------------------------------------------------------------------

function transformData(input: TransformInput) {
  const opts = input.options ?? {};

  let data: Record<string, unknown>[];
  if (input.inputFormat === 'csv') {
    if (typeof input.data !== 'string') throw new Error('CSV input must be a string');
    data = parseCSV(input.data, opts.delimiter, opts.headers ?? true);
  } else {
    if (!Array.isArray(input.data)) throw new Error('JSON input must be an array');
    data = input.data;
  }

  const inputRows = data.length;

  if (opts.filter) data = applyFilter(data, opts.filter);
  if (opts.aggregate) data = aggregate(data, opts.aggregate);
  if (opts.select || opts.rename) data = selectAndRename(data, opts.select, opts.rename);

  const outputData = input.outputFormat === 'csv' ? toCSV(data, opts.delimiter) : data;
  const mimeType = input.outputFormat === 'csv' ? 'text/csv' : 'application/json';

  return { data: outputData, mimeType, stats: { inputRows, outputRows: data.length, inputFormat: input.inputFormat, outputFormat: input.outputFormat } };
}

// ---------------------------------------------------------------------------
// CSV parsing / serialization
// ---------------------------------------------------------------------------

function parseCSV(csv: string, delimiter = ',', hasHeaders = true): Record<string, unknown>[] {
  if (csv.trim() === '') return [];

  const rows: string[][] = [];
  let current: string[] = [];
  let field = '';
  let inQuotes = false;
  let fieldWasQuoted = false;

  const pushField = () => { current.push(fieldWasQuoted ? field : field.trim()); field = ''; fieldWasQuoted = false; };
  const pushRow = () => { rows.push(current); current = []; };

  for (let i = 0; i < csv.length; i++) {
    const char = csv[i];
    if (inQuotes) {
      if (char === '"') {
        if (csv[i + 1] === '"') { field += '"'; i++; } else { inQuotes = false; }
      } else { field += char; }
    } else if (char === '"' && field === '') {
      inQuotes = true; fieldWasQuoted = true;
    } else if (char === delimiter) {
      pushField();
    } else if (char === '\n' || char === '\r') {
      pushField(); pushRow();
      if (char === '\r' && csv[i + 1] === '\n') i++;
    } else { field += char; }
  }
  if (inQuotes) throw new Error('Unclosed quoted field in CSV input');
  if (field.length > 0 || current.length > 0) { pushField(); pushRow(); }

  const cleaned = rows.filter(r => r.some(c => c !== '') || r.length > 1);
  if (cleaned.length === 0) return [];

  const headers = hasHeaders ? cleaned[0] : cleaned[0].map((_, i) => `column_${i + 1}`);
  const dataRows = hasHeaders ? cleaned.slice(1) : cleaned;

  return dataRows.map(row => {
    const obj: Record<string, unknown> = {};
    headers.forEach((h, i) => {
      let v: unknown = row[i] ?? '';
      if (typeof v === 'string' && v !== '') { const n = Number(v); if (!isNaN(n)) v = n; }
      obj[h] = v;
    });
    return obj;
  });
}

function toCSV(data: Record<string, unknown>[], delimiter = ','): string {
  if (data.length === 0) return '';
  const headers = Object.keys(data[0]);
  const esc = (v: unknown) => {
    const s = String(v ?? '');
    return s.includes(delimiter) || s.includes('"') || s.includes('\n') ? `"${s.replace(/"/g, '""')}"` : s;
  };
  return [headers.join(delimiter), ...data.map(r => headers.map(h => esc(r[h])).join(delimiter))].join('\n');
}

// ---------------------------------------------------------------------------
// Transformations
// ---------------------------------------------------------------------------

function applyFilter(data: Record<string, unknown>[], f: FilterOptions): Record<string, unknown>[] {
  return data.filter(row => {
    const v = row[f.field], c = f.value;
    switch (f.operator) {
      case 'eq': return v === c;
      case 'ne': return v !== c;
      case 'gt': return Number(v) > Number(c);
      case 'lt': return Number(v) < Number(c);
      case 'gte': return Number(v) >= Number(c);
      case 'lte': return Number(v) <= Number(c);
      case 'contains': return String(v).includes(String(c));
      case 'startsWith': return String(v).startsWith(String(c));
      case 'endsWith': return String(v).endsWith(String(c));
      default: return true;
    }
  });
}

function selectAndRename(data: Record<string, unknown>[], select?: string[], rename?: Record<string, string>): Record<string, unknown>[] {
  return data.map(row => {
    const out: Record<string, unknown> = {};
    for (const f of select ?? Object.keys(row)) { if (f in row) out[rename?.[f] ?? f] = row[f]; }
    return out;
  });
}

function aggregate(data: Record<string, unknown>[], opts: AggregateOptions): Record<string, unknown>[] {
  const groups = new Map<string, Record<string, unknown>[]>();
  for (const row of data) {
    const key = String(row[opts.groupBy] ?? 'null');
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(row);
  }
  const result: Record<string, unknown>[] = [];
  for (const [key, rows] of groups.entries()) {
    const agg: Record<string, unknown> = { [opts.groupBy]: key };
    for (const op of opts.operations) {
      const vals = rows.map(r => Number(r[op.field])).filter(v => !isNaN(v));
      const alias = op.alias ?? `${op.operation}_${op.field}`;
      switch (op.operation) {
        case 'sum': agg[alias] = vals.reduce((a, b) => a + b, 0); break;
        case 'avg': agg[alias] = vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : 0; break;
        case 'min': agg[alias] = vals.length ? Math.min(...vals) : 0; break;
        case 'max': agg[alias] = vals.length ? Math.max(...vals) : 0; break;
        case 'count': agg[alias] = rows.length; break;
      }
    }
    result.push(agg);
  }
  return result;
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FilterOptions {
  field: string;
  operator: 'eq' | 'ne' | 'gt' | 'lt' | 'gte' | 'lte' | 'contains' | 'startsWith' | 'endsWith';
  value: unknown;
}

interface AggregateOptions {
  groupBy: string;
  operations: Array<{ field: string; operation: 'sum' | 'avg' | 'min' | 'max' | 'count'; alias?: string }>;
}

interface TransformOptions {
  delimiter?: string;
  headers?: boolean;
  filter?: FilterOptions;
  select?: string[];
  rename?: Record<string, string>;
  aggregate?: AggregateOptions;
}

interface TransformInput {
  kind?: string;
  data: string | Record<string, unknown>[];
  inputFormat: 'csv' | 'json';
  outputFormat: 'csv' | 'json';
  options?: TransformOptions;
}

function extractTransformInput(task: StartTaskMessage): TransformInput | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (p !== null && typeof p === 'object' && 'data' in p && 'inputFormat' in p && 'outputFormat' in p) {
      return p as TransformInput;
    }
  }
  return undefined;
}
