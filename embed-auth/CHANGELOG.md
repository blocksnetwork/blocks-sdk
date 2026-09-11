# Changelog

## 0.1.0 — Unreleased

- Initial widget package skeleton.

### Fixed

- Embedded pages deployed to a static host (Vercel, Cloudflare, Netlify) now
  resolve runtime configuration from the same Blocks backend used for
  sign-in. Previously, task calls could be routed to the public Blocks
  Network and fail with "Agent not found" for agents that live on a
  dedicated Blocks backend.
