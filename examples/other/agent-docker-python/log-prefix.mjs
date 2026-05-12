#!/usr/bin/env node
// Prepends `HH:MM:ss.lll` to each line of stdin and writes to stdout.
// Mirrors the backend's pino-pretty `translateTime: 'SYS:HH:MM:ss.l'` so
// agent docker logs and `npm run dev` backend logs share a clock format.

process.stdin.setEncoding('utf8');
let buf = '';

const pad = (n, len = 2) => String(n).padStart(len, '0');
function stamp() {
  const d = new Date();
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

process.stdin.on('data', (chunk) => {
  buf += chunk;
  const lines = buf.split('\n');
  buf = lines.pop() ?? '';
  for (const line of lines) {
    process.stdout.write(`[${stamp()}] ${line}\n`);
  }
});

process.stdin.on('end', () => {
  if (buf.length > 0) process.stdout.write(`[${stamp()}] ${buf}\n`);
});
