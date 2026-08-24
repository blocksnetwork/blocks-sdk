/**
 * Popup orchestration for `@blocks-network/embed-auth`.
 *
 * Owns: state-nonce generation, popup-URL construction, `window.open`
 * wrapper, `message` listener with origin / schema / state correlation
 * defenses, set-equality + 1:1 UUID mapping checks on the success
 * envelope, the single-popup-in-flight policy, and the
 * 5-minute timeout fallback.
 *
 * Out of scope: the SDK token provider, refresh manager, storage, and
 * the public `signInAndGetClient(s)` surface — those live in
 * `refresh.ts`, `storage.ts`, and `api.ts`.
 */

import {
  EMBED_POPUP_NAME,
  POPUP_CLOSE_POLL_MS,
  POPUP_CLOSE_GRACE_MS,
  POPUP_TIMEOUT_MS,
  POPUP_WIDTH,
  POPUP_HEIGHT,
  STATE_NONCE_BYTES,
} from './constants.js';
import {
  validateSuccessEnvelope,
  validateErrorEnvelope,
} from './schemas.js';
import {
  BlocksAuthError,
  type BlocksAuthSuccessEnvelope,
  type BlocksAuthErrorEnvelope,
} from './types.js';

export interface OpenPopupArgs {
  /** Bare agent names (caller is responsible for sorting / dedup). */
  agents: string[];
  /** Resolved Blocks backend base URL, e.g. `https://blocks.ai`. */
  backendBaseUrl: string;
  /** `window.location.origin` at call time. Sent as `replyOrigin` query
   *  parameter; the popup uses it as the postMessage `targetOrigin`. */
  pageOrigin: string;
}

export interface PopupResult {
  envelope: BlocksAuthSuccessEnvelope;
  state: string;
}

interface PopupRecord {
  state: string;
  resolve: (result: PopupResult) => void;
  reject: (error: BlocksAuthError) => void;
  listener: (event: MessageEvent) => void;
  timeoutId: ReturnType<typeof setTimeout>;
  closePollId: ReturnType<typeof setInterval>;
  /** Set when a popup close is observed; the deferred USER_CANCELLED reject.
   *  Cleared by `cleanup` if a message settles the promise within the grace. */
  closeGraceId?: ReturnType<typeof setTimeout>;
  backendOrigin: string;
  /** The window handle returned by `window.open`. Used as a sender
   *  identity check in the message listener: a forged success/error
   *  envelope from any OTHER window served on `backendOrigin` (a
   *  second tab, an iframe) is dropped because `event.source` won't
   *  match this handle. The `state` nonce is a correlation token, not
   *  sender authentication — it travels to the popup as a URL query
   *  param and is readable in-document, so it cannot stand alone. */
  popupWindow: Window;
}

/**
 * Module-local single-popup-in-flight registry, keyed by resolved
 * backend origin. A second concurrent call for the same
 * key replaces the older record; the older promise rejects with
 * `POPUP_REPLACED` and its listener / timeout are torn down.
 */
const inFlightPopups = new Map<string, PopupRecord>();

function generateStateNonce(): string {
  const bytes = new Uint8Array(STATE_NONCE_BYTES);
  // jsdom and modern browsers both expose `crypto.getRandomValues`.
  crypto.getRandomValues(bytes);
  return base64UrlEncode(bytes);
}

function base64UrlEncode(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]!);
  // `btoa` is available in jsdom + browsers; encoded → url-safe (no `=` padding).
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function buildPopupUrl(args: OpenPopupArgs, state: string): string {
  const params = new URLSearchParams();
  params.set('agents', args.agents.join(','));
  params.set('replyOrigin', args.pageOrigin);
  params.set('state', state);
  // Trim trailing slash so we don't double-up before /api/...
  const base = args.backendBaseUrl.replace(/\/+$/, '');
  return `${base}/api/v1/auth/embed/popup?${params.toString()}`;
}

function resolveBackendOrigin(backendBaseUrl: string): string {
  return new URL(backendBaseUrl).origin;
}

/** Centralized teardown — every settle path goes through this so the
 *  message listener / timeout cannot leak. */
function cleanup(record: PopupRecord): void {
  clearTimeout(record.timeoutId);
  clearInterval(record.closePollId);
  if (record.closeGraceId !== undefined) clearTimeout(record.closeGraceId);
  window.removeEventListener('message', record.listener);
  if (inFlightPopups.get(record.backendOrigin) === record) {
    inFlightPopups.delete(record.backendOrigin);
  }
}

/** Returns null on success, an error code on mismatch. Centralized so
 *  popup tests can exercise each branch through the public surface.
 *
 *  The backend popup intentionally returns the INTERSECTION of
 *  requested agents and agents the user can actually reach
 *  (`canUserReachAgent` — public OR org-member OR grantee). A page
 *  listing `[public, privateNotGranted]` receives `[public]` in the
 *  success envelope; the widget must accept that narrowed scope so
 *  the public agent still works. We therefore validate that the
 *  returned set is a NON-EMPTY SUBSET of the requested set (plus the
 *  internal 1:1 agentIds↔agents[*].id invariant). The all-zero case
 *  is not reachable here — the backend short-circuits to an
 *  `AGENT_ARCHIVED` error envelope when zero agents are reachable
 *  (privacy-preserving reuse of the soft-delete code; there is
 *  deliberately no separate `AGENT_NOT_REACHABLE`
 *  code so the popup is not an enumeration oracle).
 *
 *  Name comparison is case-sensitive — the backend validator
 *  (`^[a-zA-Z0-9_]+$`) and DB unique index treat `Foo` and `foo` as
 *  distinct agents, and the storage partition key preserves case (see
 *  `storage.ts`). Folding here would silently accept `foo` when the
 *  page requested `Foo`, returning a different agent than asked for.
 */
function checkAgentSetMatch(
  requested: readonly string[],
  envelope: BlocksAuthSuccessEnvelope,
): 'AGENT_SET_MISMATCH' | null {
  const wantNames = requested;
  const gotNames = envelope.agents.map((a) => a.name);
  const wantSet = new Set(wantNames);
  const gotSet = new Set(gotNames);
  if (wantSet.size !== wantNames.length) return 'AGENT_SET_MISMATCH';
  if (gotSet.size !== gotNames.length) return 'AGENT_SET_MISMATCH';
  if (gotSet.size === 0) return 'AGENT_SET_MISMATCH';
  // Every returned agent must have been requested. (Subset, not equal —
  // the reverse direction is allowed and represents legitimate scope
  // narrowing on the server.)
  for (const n of gotSet) if (!wantSet.has(n)) return 'AGENT_SET_MISMATCH';

  // 1:1 mapping between agentIds and agents[*].id.
  const idsFromAgents = envelope.agents.map((a) => a.id);
  if (idsFromAgents.length !== envelope.agentIds.length) {
    return 'AGENT_SET_MISMATCH';
  }
  const agentIdSet = new Set(envelope.agentIds);
  if (agentIdSet.size !== envelope.agentIds.length) {
    return 'AGENT_SET_MISMATCH';
  }
  const agentEntryIdSet = new Set(idsFromAgents);
  if (agentEntryIdSet.size !== idsFromAgents.length) {
    return 'AGENT_SET_MISMATCH';
  }
  for (const id of agentIdSet) if (!agentEntryIdSet.has(id)) return 'AGENT_SET_MISMATCH';
  return null;
}

export async function openPopupAndAwaitEnvelope(
  args: OpenPopupArgs,
): Promise<PopupResult> {
  const backendOrigin = resolveBackendOrigin(args.backendBaseUrl);
  const state = generateStateNonce();
  const url = buildPopupUrl(args, state);

  // Replace any in-flight popup for the same backend origin.
  const existing = inFlightPopups.get(backendOrigin);
  if (existing) {
    const replaceErr = new BlocksAuthError('POPUP_REPLACED');
    cleanup(existing);
    existing.reject(replaceErr);
  }

  const popupWindow = window.open(
    url,
    EMBED_POPUP_NAME,
    `width=${POPUP_WIDTH},height=${POPUP_HEIGHT}`,
  );
  if (!popupWindow) throw new BlocksAuthError('POPUP_BLOCKED');

  return new Promise<PopupResult>((resolve, reject) => {
    const record: PopupRecord = {
      state,
      resolve,
      reject,
      backendOrigin,
      popupWindow,
      // Filled in below — `record` is captured by the listener/timeout
      // closures, which need to call `cleanup(record)`.
      listener: () => {},
      timeoutId: setTimeout(() => {
        cleanup(record);
        reject(new BlocksAuthError('USER_CANCELLED'));
      }, POPUP_TIMEOUT_MS),
      // Poll for a manual popup close. A user-closed popup posts no
      // message and fires no timeout, so without this the promise would
      // hang until POPUP_TIMEOUT_MS. On detected close, settle the same
      // way the Cancel button + timeout do: USER_CANCELLED.
      closePollId: setInterval(() => {
        if (popupWindow.closed) {
          // Stop polling immediately, but don't reject yet: the success page
          // posts its message then closes in one task, so a queued success
          // event may still be pending. Give it a grace tick; if a message
          // settles the promise first, `cleanup` clears this timeout. (Fix #7.)
          clearInterval(record.closePollId);
          record.closeGraceId = setTimeout(() => {
            cleanup(record);
            reject(new BlocksAuthError('USER_CANCELLED'));
          }, POPUP_CLOSE_GRACE_MS);
        }
      }, POPUP_CLOSE_POLL_MS),
      closeGraceId: undefined,
    };

    record.listener = (event: MessageEvent) => {
      // Drop silently when origin doesn't match. Many unrelated postMessages
      // hit a window in the wild; this is a correlation defense, not an error.
      if (event.origin !== backendOrigin) return;
      // Sender-identity check: the envelope MUST come from the popup we
      // opened, not from any other window/iframe served on the same
      // backend origin. `event.source` is the posting window; comparing
      // it to our retained handle closes the gap left by origin-only
      // filtering. The `state` nonce alone is insufficient — it is
      // delivered to the popup as a readable URL query param.
      if (event.source !== record.popupWindow) return;

      const data = event.data;
      if (validateSuccessEnvelope(data)) {
        if (data.state !== state) return; // popup-race correlation
        const mismatch = checkAgentSetMatch(args.agents, data);
        if (mismatch) {
          cleanup(record);
          reject(new BlocksAuthError(mismatch));
          return;
        }
        cleanup(record);
        resolve({ envelope: data, state });
        return;
      }
      if (validateErrorEnvelope(data)) {
        if (data.state !== state) return;
        cleanup(record);
        reject(buildErrorFromEnvelope(data));
        return;
      }
      // Schema-invalid → drop silently (likely an unrelated postMessage).
    };

    window.addEventListener('message', record.listener);
    inFlightPopups.set(backendOrigin, record);
  });
}

function buildErrorFromEnvelope(env: BlocksAuthErrorEnvelope): BlocksAuthError {
  return new BlocksAuthError(env.code, env.message, env.agent);
}

/**
 * Internals exposed for unit tests only. NOT part of the public API.
 * Phase 4's `api.ts` consumes only `openPopupAndAwaitEnvelope` and the
 * exported types above.
 */
export const __testing = {
  generateStateNonce,
  base64UrlEncode,
  buildPopupUrl,
  resolveBackendOrigin,
  checkAgentSetMatch,
  inFlightPopups,
};
