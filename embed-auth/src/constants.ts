/**
 * Public constants for `@blocks-network/embed-auth`.
 */

/** Stable `window.name` used for the popup; OS reuses an open window with the same name. */
export const EMBED_POPUP_NAME = 'blocks-auth-popup';

/** Popup timeout fallback (5 minutes). */
export const POPUP_TIMEOUT_MS = 5 * 60 * 1000;

/**
 * Interval for polling `popupWindow.closed`. A manually-closed popup
 * sends no message and fires no timer, so without this poll the
 * sign-in promise would hang until POPUP_TIMEOUT_MS. 500ms is
 * responsive to a human close without busy-spinning.
 */
export const POPUP_CLOSE_POLL_MS = 500;

/**
 * After first observing `popupWindow.closed`, wait this long before settling
 * as USER_CANCELLED. The success page posts its message then closes in the
 * same task; the queued `message` event may not have dispatched when the poll
 * first sees `closed`. This grace tick lets a successful sign-in resolve
 * instead of being misreported as a cancel (which would orphan the
 * just-minted refresh-token row).
 */
export const POPUP_CLOSE_GRACE_MS = 250;

/** Popup window dimensions. */
export const POPUP_WIDTH = 480;
export const POPUP_HEIGHT = 600;

/** State nonce length in raw bytes (16 = 128 bits ≈ 22 base64 chars). */
export const STATE_NONCE_BYTES = 16;

/** Maximum number of agents allowed in a single sign-in call (matches wire schemas). */
export const MAX_AGENTS = 25;

/** `localStorage` partition key prefix. */
export const STORAGE_KEY_PREFIX = 'blocks-auth-session-v1';

/** `localStorage` key holding the active-sessions index. */
export const ACTIVE_SESSIONS_KEY = 'blocks-auth-active-sessions-v1';

/**
 * Build-time injected default Blocks backend base URL. The Rollup
 * `@rollup/plugin-replace` step substitutes `__BACKEND_BASE_URL_DEFAULT__`
 * with a JSON-stringified URL at bundle time. The fallback string keeps
 * `tsc --noEmit`, vitest, and IDE tooling happy when the replace plugin
 * has not run.
 */
declare const __BACKEND_BASE_URL_DEFAULT__: string | undefined;

export const BACKEND_BASE_URL_DEFAULT: string =
  typeof __BACKEND_BASE_URL_DEFAULT__ !== 'undefined'
    ? __BACKEND_BASE_URL_DEFAULT__
    : 'https://app.blocks.ai';
