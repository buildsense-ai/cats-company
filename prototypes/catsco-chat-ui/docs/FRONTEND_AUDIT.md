# Mini Chat Frontend Audit

Date: 2026-07-07

## Current Shape

This frontend is a high-fidelity prototype in a single `index.html`.

- Total file size: about 125 KB
- Total lines: about 5,015
- Inline CSS: about 2,404 lines
- Inline JavaScript: about 2,343 lines
- JavaScript functions: about 80
- Backend: lightweight Python proxy in `server.py`
- Backend API surface: `/api/health`, `/api/new`, `/api/chat`, `/api/sessions`, `/api/history/{id}`, `/api/rename/{id}`, `/api/delete/{id}`

This is good for fast UX iteration, but it is now near the limit where further feature work will become harder unless the structure is stabilized.

## What Is Working

- Core chat flow works through the local Python backend.
- Session list, pinned items, rename/delete/share actions, group/friend mock lists, search, theme switching, collapsed sidebar, progress navigation, and stop generation all exist.
- The UI direction is now much clearer than the original CatsCo style: quieter, denser, more desktop-tool oriented.
- There is already a late-stage UI normalization layer for shared radius, font size, spacing, hover, active, and disabled states.

## Main Structural Risks

### 1. CSS Cascade Is Too Deep

The same components are styled in several distant sections:

- sidebar rows
- group/friend rows
- session rows
- action buttons
- theme-specific overrides
- final normalization layer

This makes visual fixes fragile. A later selector can silently override an earlier one.

Recommendation: before more visual tuning, move toward grouped component sections or design tokens with a clear override order.

### 2. State And DOM Are Mixed Together

The JavaScript currently holds app state, renders DOM, binds events, calls APIs, stores local data, and manages UI effects in the same file.

Examples of mixed responsibilities:

- `renderSidebar()` handles pinned sessions, history, groups, friends, collapsed state, and empty states.
- `renderMessages()` handles welcome view, message cards, process panels, actions, progress bar, and scroll behavior.
- `send()` handles local message mutation, backend streaming, process steps, errors, persistence, and button state.

Recommendation: split by domain before adding production-level CatsCo features.

### 3. Mock Data And Real Data Are Blended

Current conversations call the local backend. Groups and friends are local-only mock data in `localStorage`.

This is acceptable for UX prototyping, but for replacing CatsCo it needs a clear adapter boundary:

- local prototype adapter
- CatsCo backend adapter
- mock/demo fallback adapter

### 4. Inline Event Handlers Should Be Phased Out

The HTML still contains inline handlers such as `onclick`, `oninput`, and `onkeydown`. This works, but it makes component extraction harder.

Recommendation: when refactoring, bind events from JavaScript modules or framework components.

### 5. Generated HTML Uses `innerHTML` Heavily

The current UI uses `innerHTML` for menus, QR modal, message cards, process panels, and icons.

This is fast for a prototype. For a production replacement, message content and user-provided text should have stricter rendering boundaries.

## Suggested Refactor Order

1. Keep the current `index.html` as the visual source of truth.
2. Extract API access into a `chatApi` layer.
3. Extract state shape into explicit models: `session`, `message`, `group`, `friend`, `user`, `ui`.
4. Extract rendering into component functions before choosing React.
5. Once stable, migrate to React components if the company wants a production CatsCo replacement.

## Do Not Do Yet

- Do not rewrite everything immediately.
- Do not import the full `cats-company` frontend wholesale.
- Do not keep adding one-off CSS fixes without checking the token layer.
- Do not connect to the production CatsCo backend until the adapter shape is decided.
