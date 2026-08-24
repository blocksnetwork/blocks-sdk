# @blocks-network/embed-auth

Drop-in browser widget for [Blocks Network](https://blocks.ai) embedded
authentication. Partner-hosted pages load the widget, call
`signInAndGetClient(s)` to authenticate the user via popup, and receive a
ready `TaskClient` (or `Record<string, TaskClient>`) for one or more
agents.

This package is the public-facing surface for embedded auth; the
backend-side popup, refresh, and revoke endpoints are served by the
Blocks backend.

## What it does, what it doesn't

- **Does:** popup-based sign-in, automatic refresh-token loop with one
  shared timer per page, partitioned `localStorage` so multiple partner
  pages on the same browser don't collide, and best-effort `signOut`
  with idempotent backend revoke.
- **Doesn't:** ship JWTs to disk (TTL is ~60s — pointless to persist),
  manage agent metadata (that's the dashboard's job), or enable
  cross-tenant sign-in (the popup enforces single-org agent sets).

## Usage

### IIFE (`<script>` tag)

```html
<script src="https://app.blocks.ai/embed/auth.0.1.0.min.js"></script>
<script>
  (async () => {
    const client = await window.BlocksAuth.signInAndGetClient({
      agent: 'translator',
    });
    // `client` is a ready Blocks `TaskClient`. Call `sendMessage` etc.
  })();
</script>
```

### ESM (bundlers)

```ts
import { signInAndGetClient } from '@blocks-network/embed-auth';

const client = await signInAndGetClient({ agent: 'translator' });
```

You also need `@blocks-network/sdk` installed (peer dependency for ESM/CJS
consumers). The IIFE bundle ships with the SDK inlined.

### Multi-agent

```ts
import { signInAndGetClients } from '@blocks-network/embed-auth';

const clients = await signInAndGetClients({
  agents: ['translator', 'summarizer', 'classifier'],
});
// `clients` is `Record<string, TaskClient>`. All clients share one
// refresh loop; multiple agents at the same `billingMode` share one
// underlying `TaskClient` (keyed by name).
await clients.translator.sendMessage({ /* ... */ });
```

### Sign-out

```ts
import { signOut } from '@blocks-network/embed-auth';

// Revoke every active embedded-auth session under window.location.origin.
await signOut();
```

`signOut` is always argless. Sign-out is whole-Blocks-on-this-page; if
you want to switch the page to a narrower agent set, call `signIn*`
again with the new list — leaving a refresh token alive while you
"partially log out" is not a supported flow.

For cross-origin logout (every embed-auth site, on every device), the
user signs out of `blocks.ai` directly. That path sets the user's
`embedded_auth_revoked_after` watermark server-side and invalidates
every still-live refresh token without widget cooperation.

`signOut` is best-effort: a network failure on the backend revoke is
swallowed (revoke is idempotent and the JWT TTL caps any damage), but
`localStorage` is always cleared so the local state cannot drift from
the user's intent.

### Error handling

Every rejection is a typed `BlocksAuthError`:

```ts
import { BlocksAuthError, signInAndGetClient } from '@blocks-network/embed-auth';

try {
  const client = await signInAndGetClient({ agent: 'translator' });
} catch (err) {
  if (err instanceof BlocksAuthError) {
    switch (err.code) {
      case 'POPUP_BLOCKED':         /* ask the user to allow popups */ break;
      case 'USER_CANCELLED':        /* user closed the popup */ break;
      case 'AGENT_DISABLED':        /* admin turned the agent off */ break;
      // see `BlocksAuthErrorCode` for the full list.
    }
  }
}
```

## Security model / threat model

**The JWT is never persisted.** It lives in memory for its ~60s lifetime
(`EmbeddedAuthSessionManager`); on reload the widget silently re-mints from
the refresh token. Nothing writes `token` / `jwt` / `expiresAt` to disk.

**The refresh token IS persisted in `localStorage`** — this is a deliberate,
ratified tradeoff, not an oversight. The refresh token is the longer-lived
credential (up to 24h), and `localStorage` is readable by any script running
on the partner page, including third-party scripts the page author pastes in
(analytics, chat widgets, tag managers). An XSS on the partner page can
exfiltrate it. We accept this because the alternatives don't fit a *static*
third-party page:

- **`HttpOnly` cookie scoped to the refresh endpoint** — the textbook XSS
  defense, but the refresh endpoint is cross-origin to the partner page, so
  this is a third-party cookie. Safari ITP and the deprecation of
  third-party cookies make it unreliable, and the whole embed surface
  deliberately uses bearer tokens + `credentials: 'omit'` to sidestep
  cross-site cookie rules.
- **In-memory only + silent re-auth on reload** — strongest, but forces a
  popup/redirect on every page load (popups-on-load are widely blocked
  unless tied to a user gesture), which defeats the drop-in UX. A candidate
  opt-in "high-security mode" for embedders who want it.
- **Non-extractable Web Crypto key** — does not apply: a refresh token is a
  bearer string that must be sent verbatim, so XSS can still read and replay
  it.

The exposure is bounded by mitigations that cap the blast radius rather than
eliminate it:

- **24h max refresh TTL**, decaying monotonically — refresh chains can only
  shrink toward the ceiling set at popup mint, never extend it.
- **Single-use rotation** — each refresh revokes the submitted token and
  issues a new one; a stolen token is replayable for at most one cycle, and
  a race between the legitimate widget and an attacker invalidates both
  beyond the first use.
- **Per-`agentIds` scope** — a stolen token mints JWTs only for the agents
  the page listed, not the user's whole account.
- **Kill switch + `embedded_auth_revoked_after` watermark** — provider- and
  user-initiated revocation invalidate live refresh tokens server-side.
- **≤60s JWT TTL + ≤5s liveness recheck** — even a freshly minted JWT loses
  access within seconds of a kill/soft-delete/grant revocation.

Providers hosting on a partner origin are taking on that origin's XSS risk;
this is the responsibility boundary of the static-page model. Partner apps
that need stronger isolation should graduate to the customer-backend-proxy
pattern (Mode 2 `tokenEndpoint`), where the refresh credential never reaches
the browser.

**Popup message authentication.** The widget's `message` listener accepts an
envelope only when (a) `event.origin` is the Blocks backend origin, (b)
`event.source` is the exact popup window handle the widget opened, and (c)
the `state` nonce matches the in-flight request. The `event.source` check is
the sender-identity gate: a forged envelope from any other window or iframe
served on the backend origin is dropped even if it guesses the nonce (the
nonce travels to the popup as a readable URL query param, so it is a
correlation token, not authentication).

## License

PubNub.
