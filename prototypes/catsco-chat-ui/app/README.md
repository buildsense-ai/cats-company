# Frontend Skeleton

This folder contains the frontend runtime files extracted from `index.html`.

## Files

- `runtime-shims.js`
  Small browser compatibility shims loaded before the UI. It provides fallback `marked`, `hljs`, and `DOMPurify` objects when external libraries are not present.

- `styles.css`
  Stylesheet manifest. It keeps the import order for the split CSS layers in `styles/`.

- `styles/`
  Split CSS layers for design tokens, sidebar, chat layout, composer, shared controls, settings panels, light-mode repair, and responsive rules.

- `state.js`
  Shared frontend state for sessions, groups, friends, model status, theme, and active stream.

- `api.js`
  Local backend adapter. All direct `/api/*` requests should live here so the frontend can later swap to a CatsCo backend adapter.

- `utils.js`
  Shared rendering and escaping helpers.

- `theme.js`
  Theme switching and the subtle pointer background effect.

- `sidebar.js`
  Sidebar, session list, group/friend list, account menu, sharing, and QR related UI helpers.

- `messages.js`
  Message rendering, code copy buttons, message menus, progress navigation, and regenerate action.

- `task-process.js`
  Event-driven task progress model, plain-language step planning, legacy process migration, completion/error/stop states, and the two-level process renderer.

- `input.js`
  Input resizing, send button state, send/stop behavior, stream handling, and keyboard shortcuts.

- `search.js`
  Welcome sample prompts and sidebar search results.

- `settings-panels.js`
  Virtual settings panels for feedback, desktop connection, downloads, sample tasks, relay service, profile, and logout.

- `main.js`
  Current application logic: session operations, backend health check, and startup wiring.

## Current Rule

Keep `index.html` as structure only. New behavior should move toward smaller files instead of adding large inline scripts or styles back into the HTML.

## Next Split

Recommended next files:

- `menus.js`
- `cats-company-api-binding.md`
