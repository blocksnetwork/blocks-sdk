// node-fetch and undici both connect via net.Socket.prototype.connect; install before clients.
import net from 'node:net';

type HostFilter = (host: string) => boolean;

const liveSockets = new Map<net.Socket, string>();
let isBlocked: HostFilter | null = null;
let installed = false;

function hostOf(args: unknown[]): string {
  const first = Array.isArray(args[0]) ? args[0][0] : args[0];
  if (first && typeof first === 'object') {
    const opts = first as { host?: string; servername?: string };
    return opts.servername ?? opts.host ?? 'localhost';
  }
  return typeof args[1] === 'string' ? args[1] : 'localhost';
}

function refusal(host: string): Error {
  return Object.assign(new Error(`network blackhole: ${host}`), { code: 'ECONNREFUSED' });
}

export function installBlackhole(): void {
  if (installed) return;
  installed = true;
  const original = net.Socket.prototype.connect;
  net.Socket.prototype.connect = function patchedConnect(this: net.Socket, ...args: unknown[]) {
    const host = hostOf(args);
    if (isBlocked?.(host)) {
      process.nextTick(() => this.destroy(refusal(host)));
      return this;
    }
    liveSockets.set(this, host);
    this.once('close', () => liveSockets.delete(this));
    return (original as (...a: unknown[]) => net.Socket).apply(this, args);
  } as typeof original;
}

export function goDark(filter: HostFilter = () => true): void {
  isBlocked = filter;
  for (const [socket, host] of liveSockets) {
    if (filter(host)) socket.destroy(refusal(host));
  }
}

export function restoreNetwork(): void {
  isBlocked = null;
}
