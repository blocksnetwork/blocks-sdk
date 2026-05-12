import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Report Generator Handler
 *
 * Generates formatted reports from structured data.
 * Supports multiple output formats: Markdown, HTML, plain text.
 *
 * Input format:
 * {
 *   kind: 'report',
 *   title: 'Monthly Sales Report',
 *   sections: [
 *     {
 *       heading: 'Summary',
 *       type: 'text' | 'table' | 'list' | 'metrics',
 *       content: ... // varies by type
 *     }
 *   ],
 *   format?: 'markdown' | 'html' | 'text',
 *   options?: {
 *     author?: string,
 *     includeTimestamp?: boolean,
 *     includeToc?: boolean
 *   }
 * }
 *
 * Output: Formatted report in the specified format
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = extractReportInput(task);

  if (!input) {
    const artifact = {
      ok: false,
      error: 'Missing report input with title and sections',
      example: {
        kind: 'report',
        title: 'Monthly Sales Report',
        sections: [
          {
            heading: 'Summary',
            type: 'text',
            content: 'This report summarizes monthly sales performance.',
          },
          {
            heading: 'Key Metrics',
            type: 'metrics',
            content: [
              { label: 'Revenue', value: 125000, unit: 'USD', change: 15 },
              { label: 'Orders', value: 450, change: 8 },
            ],
          },
        ],
        format: 'markdown',
      },
    };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }

  const format = input.format ?? 'markdown';
  console.log(`[ReportGenerator] Generating ${format} report: ${input.title}`);
  ctx?.reportStatus(`Generating ${format} report`);

  try {
    let content: string;
    let mimeType: string;

    switch (format) {
      case 'html':
        content = generateHTML(input);
        mimeType = 'text/html';
        break;
      case 'text':
        content = generateText(input);
        mimeType = 'text/plain';
        break;
      case 'markdown':
      default:
        content = generateMarkdown(input);
        mimeType = 'text/markdown';
        break;
    }

    console.log(`[ReportGenerator] Generated ${format} report with ${input.sections.length} sections`);
    return { artifact: content, mimeType };
  } catch (err) {
    const artifact = { ok: false, error: (err as Error).message };
    return { artifact: JSON.stringify(artifact, null, 2), mimeType: 'application/json' };
  }
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface MetricItem {
  label: string;
  value: number | string;
  unit?: string;
  change?: number;
  changeLabel?: string;
}

interface TableData {
  headers: string[];
  rows: (string | number)[][];
}

interface ReportSection {
  heading: string;
  type: 'text' | 'table' | 'list' | 'metrics';
  content: string | string[] | TableData | MetricItem[];
}

interface ReportInput {
  kind?: string;
  title: string;
  subtitle?: string;
  sections: ReportSection[];
  format?: 'markdown' | 'html' | 'text';
  options?: {
    author?: string;
    includeTimestamp?: boolean;
    includeToc?: boolean;
    theme?: 'light' | 'dark';
  };
}

// ---------------------------------------------------------------------------
// Input extraction
// ---------------------------------------------------------------------------

function extractReportInput(task: StartTaskMessage): ReportInput | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (p !== null && typeof p === 'object' && 'title' in p && 'sections' in p && Array.isArray((p as ReportInput).sections)) {
      return p as ReportInput;
    }
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Markdown generator
// ---------------------------------------------------------------------------

function generateMarkdown(report: ReportInput): string {
  const lines: string[] = [];
  const opts = report.options ?? {};

  lines.push(`# ${report.title}`);
  if (report.subtitle) lines.push(`*${report.subtitle}*`);
  lines.push('');

  if (opts.author || opts.includeTimestamp) {
    if (opts.author) lines.push(`**Author:** ${opts.author}`);
    if (opts.includeTimestamp) lines.push(`**Generated:** ${new Date().toISOString()}`);
    lines.push('');
  }

  if (opts.includeToc && report.sections.length > 0) {
    lines.push('## Table of Contents');
    report.sections.forEach((section, i) => {
      const anchor = section.heading.toLowerCase().replace(/[^a-z0-9]+/g, '-');
      lines.push(`${i + 1}. [${section.heading}](#${anchor})`);
    });
    lines.push('');
  }

  for (const section of report.sections) {
    lines.push(`## ${section.heading}`);
    lines.push('');

    switch (section.type) {
      case 'text':
        lines.push(String(section.content));
        break;

      case 'list':
        if (Array.isArray(section.content)) {
          for (const item of section.content) {
            lines.push(`- ${item}`);
          }
        }
        break;

      case 'table': {
        const table = section.content as TableData;
        if (table.headers && table.rows) {
          lines.push('| ' + table.headers.join(' | ') + ' |');
          lines.push('| ' + table.headers.map(() => '---').join(' | ') + ' |');
          for (const row of table.rows) {
            lines.push('| ' + row.join(' | ') + ' |');
          }
        }
        break;
      }

      case 'metrics': {
        const metrics = section.content as MetricItem[];
        if (Array.isArray(metrics)) {
          lines.push('| Metric | Value | Change |');
          lines.push('| --- | --- | --- |');
          for (const m of metrics) {
            const value = m.unit ? `${m.value} ${m.unit}` : String(m.value);
            const change = m.change !== undefined
              ? `${m.change > 0 ? '+' : ''}${m.change}%`
              : '-';
            lines.push(`| ${m.label} | ${value} | ${change} |`);
          }
        }
        break;
      }
    }

    lines.push('');
  }

  return lines.join('\n');
}

// ---------------------------------------------------------------------------
// HTML generator
// ---------------------------------------------------------------------------

function generateHTML(report: ReportInput): string {
  const escapeHtml = (value: string): string =>
    value.replace(/[&<>"']/g, (char) => {
      switch (char) {
        case '&': return '&amp;';
        case '<': return '&lt;';
        case '>': return '&gt;';
        case '"': return '&quot;';
        case "'": return '&#39;';
        default: return char;
      }
    });

  const opts = report.options ?? {};
  const theme = opts.theme ?? 'light';

  const styles = theme === 'dark'
    ? 'body{font-family:system-ui;max-width:800px;margin:0 auto;padding:20px;background:#1a1a2e;color:#eee}h1{color:#e94560}h2{color:#0f4c75;border-bottom:2px solid #e94560;padding-bottom:5px}table{border-collapse:collapse;width:100%}th,td{border:1px solid #444;padding:8px;text-align:left}th{background:#0f4c75}tr:nth-child(even){background:#2a2a4e}.metric-positive{color:#2ecc71}.metric-negative{color:#e74c3c}'
    : 'body{font-family:system-ui;max-width:800px;margin:0 auto;padding:20px;background:#fff;color:#333}h1{color:#2c3e50}h2{color:#3498db;border-bottom:2px solid #3498db;padding-bottom:5px}table{border-collapse:collapse;width:100%}th,td{border:1px solid #ddd;padding:8px;text-align:left}th{background:#3498db;color:#fff}tr:nth-child(even){background:#f2f2f2}.metric-positive{color:#27ae60}.metric-negative{color:#e74c3c}';

  const safeTitle = escapeHtml(report.title);

  let html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>${safeTitle}</title>
<style>${styles}</style>
</head>
<body>
<h1>${safeTitle}</h1>
`;

  if (report.subtitle) {
    html += `<p><em>${escapeHtml(report.subtitle)}</em></p>\n`;
  }

  if (opts.author || opts.includeTimestamp) {
    html += '<div class="metadata">\n';
    if (opts.author) html += `<p><strong>Author:</strong> ${escapeHtml(opts.author)}</p>\n`;
    if (opts.includeTimestamp) html += `<p><strong>Generated:</strong> ${new Date().toISOString()}</p>\n`;
    html += '</div>\n';
  }

  if (opts.includeToc && report.sections.length > 0) {
    html += '<nav><h2>Table of Contents</h2><ol>\n';
    report.sections.forEach(section => {
      const headingText = String(section.heading);
      const anchor = headingText.toLowerCase().replace(/[^a-z0-9]+/g, '-');
      html += `<li><a href="#${anchor}">${escapeHtml(headingText)}</a></li>\n`;
    });
    html += '</ol></nav>\n';
  }

  for (const section of report.sections) {
    const headingText = String(section.heading);
    const anchor = headingText.toLowerCase().replace(/[^a-z0-9]+/g, '-');
    html += `<section id="${anchor}">\n<h2>${escapeHtml(headingText)}</h2>\n`;

    switch (section.type) {
      case 'text':
        html += `<p>${escapeHtml(String(section.content)).replace(/\n/g, '<br>')}</p>\n`;
        break;

      case 'list':
        if (Array.isArray(section.content)) {
          html += '<ul>\n';
          for (const item of section.content) {
            html += `<li>${escapeHtml(String(item))}</li>\n`;
          }
          html += '</ul>\n';
        }
        break;

      case 'table': {
        const table = section.content as TableData;
        if (table.headers && table.rows) {
          html += '<table>\n<thead><tr>\n';
          for (const h of table.headers) {
            html += `<th>${escapeHtml(String(h))}</th>`;
          }
          html += '\n</tr></thead>\n<tbody>\n';
          for (const row of table.rows) {
            html += '<tr>';
            for (const cell of row) {
              html += `<td>${escapeHtml(String(cell))}</td>`;
            }
            html += '</tr>\n';
          }
          html += '</tbody></table>\n';
        }
        break;
      }

      case 'metrics': {
        const metrics = section.content as MetricItem[];
        if (Array.isArray(metrics)) {
          html += '<table>\n<thead><tr><th>Metric</th><th>Value</th><th>Change</th></tr></thead>\n<tbody>\n';
          for (const m of metrics) {
            const value = m.unit ? `${m.value} ${m.unit}` : String(m.value);
            const changeClass = m.change !== undefined
              ? (m.change > 0 ? 'metric-positive' : m.change < 0 ? 'metric-negative' : '')
              : '';
            const changeText = m.change !== undefined
              ? `${m.change > 0 ? '+' : ''}${m.change}%`
              : '-';
            const change = m.change !== undefined
              ? `<span class="${changeClass}">${escapeHtml(changeText)}</span>`
              : '-';
            html += `<tr><td>${escapeHtml(m.label)}</td><td>${escapeHtml(value)}</td><td>${change}</td></tr>\n`;
          }
          html += '</tbody></table>\n';
        }
        break;
      }
    }

    html += '</section>\n';
  }

  html += '</body>\n</html>';
  return html;
}

// ---------------------------------------------------------------------------
// Plain text generator
// ---------------------------------------------------------------------------

function generateText(report: ReportInput): string {
  const lines: string[] = [];
  const opts = report.options ?? {};
  const width = 60;

  lines.push('='.repeat(width));
  lines.push(report.title.toUpperCase());
  if (report.subtitle) lines.push(report.subtitle);
  lines.push('='.repeat(width));
  lines.push('');

  if (opts.author) lines.push(`Author: ${opts.author}`);
  if (opts.includeTimestamp) lines.push(`Generated: ${new Date().toISOString()}`);
  if (opts.author || opts.includeTimestamp) lines.push('');

  for (const section of report.sections) {
    lines.push('-'.repeat(width));
    lines.push(section.heading.toUpperCase());
    lines.push('-'.repeat(width));
    lines.push('');

    switch (section.type) {
      case 'text':
        lines.push(String(section.content));
        break;

      case 'list':
        if (Array.isArray(section.content)) {
          for (const item of section.content) {
            lines.push(`  * ${item}`);
          }
        }
        break;

      case 'table': {
        const table = section.content as TableData;
        if (table.headers && table.rows) {
          const colWidths = table.headers.map((h, i) =>
            Math.max(h.length, ...table.rows.map(r => String(r[i] ?? '').length))
          );
          const formatRow = (row: (string | number)[]) =>
            row.map((cell, i) => String(cell).padEnd(colWidths[i])).join(' | ');

          lines.push(formatRow(table.headers));
          lines.push(colWidths.map(w => '-'.repeat(w)).join('-+-'));
          for (const row of table.rows) {
            lines.push(formatRow(row));
          }
        }
        break;
      }

      case 'metrics': {
        const metrics = section.content as MetricItem[];
        if (Array.isArray(metrics)) {
          for (const m of metrics) {
            const value = m.unit ? `${m.value} ${m.unit}` : String(m.value);
            const change = m.change !== undefined
              ? ` (${m.change > 0 ? '+' : ''}${m.change}%)`
              : '';
            lines.push(`  ${m.label}: ${value}${change}`);
          }
        }
        break;
      }
    }

    lines.push('');
  }

  return lines.join('\n');
}
