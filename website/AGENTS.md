# CatsCo Website Repository Instructions

## Product scope

This repository contains the public CatsCo marketing website. It is separate from the authenticated CatsCo product workspace in `cats-company/webapp`.

CatsCo is presented as an AI employee for non-technical office users: a user gives it a goal, authorizes the relevant work environment, and receives a finished deliverable. Public copy should describe user value and control rather than internal implementation terms.

## Source of truth

Use these sources in order:

1. Current source code and automated checks.
2. `design-system/catsco/MASTER.md` for global brand and interaction rules.
3. `design-system/catsco/pages/<page>.md` for page-specific overrides.

Page overrides may specialize the master system but should not replace the CatsCo palette, typography, accessibility floor, or brand voice.

## Repository map

- `src/components/`: page and section components.
- `src/styles/base.css`: tokens, reset, shared accessibility and layout rules.
- `src/styles/site-header.css` and `site-footer.css`: shared site chrome.
- `src/styles/pages/`: page-owned styles. A page task should stay in its page component and page stylesheet whenever possible.
- `src/site-routes.ts`: route names, titles and descriptions.
- `design-system/catsco/`: design decisions and page briefs.
- `tests/`: source and route contract checks.
- `public/`: approved static brand assets and hosting fallbacks.

## Shared-file ownership

Treat these as integration-owned files. Page tasks should not modify them without calling out the dependency:

- `src/App.tsx`
- `src/site-routes.ts`
- `src/components/Header.tsx`
- `src/components/Footer.tsx`
- `src/styles/base.css`
- `src/styles/site-header.css`
- `src/styles/site-footer.css`
- `design-system/catsco/MASTER.md`
- `package.json` and lockfiles

Page-specific work belongs in the matching component, stylesheet, and design brief. Do not place new page CSS into another page's file.

## Brand and content rules

- Brand spelling is always `CatsCo`.
- Primary green is `#1A9D7A`; dark green is `#11745B`.
- Use Inter with Noto Sans SC fallback.
- The visual tone is light, calm, professional, trustworthy and future-facing.
- Avoid generic AI purple, neon glow, robots, brains, chat bubbles, heavy glassmorphism and decorative particles.
- Use real user-facing language. Avoid Agent, Runtime, RPC, LLM, tokens, compute units and similar internal vocabulary in marketing copy.
- Do not invent customer logos, compliance certifications, availability claims or performance numbers.
- Legal text remains a clearly identified pre-launch draft until reviewed against the real operating entity and data flows.

## UI and accessibility rules

- Use semantic links for navigation and buttons for actions.
- Every form control needs a visible label, useful autocomplete and inline feedback.
- Preserve visible `:focus-visible` states.
- Icon-only buttons require accessible names; decorative icons are hidden from assistive technology.
- Support `prefers-reduced-motion` and avoid `transition: all`.
- Verify 390px, 768px, 1024px and 1440px layouts.
- Do not disable zoom or hide horizontal overflow as a substitute for fixing the overflowing element.
- Images must include dimensions; below-fold non-critical images should be lazy-loaded.

## Working procedure

Before editing, check `git status --short --branch` and preserve unfamiliar changes. Keep one task per branch/worktree when work is parallel.

### Preview ownership

- Port `5175` is reserved for the shared integration preview. Start it with `pnpm run dev:shared`; the script always serves the primary worktree returned by `git worktree list`, even when invoked from a task worktree.
- A task that needs to preview unmerged work must use a distinct, explicitly assigned port: `pnpm exec vite --host 127.0.0.1 --port <port> --strictPort`. Report both the port and worktree in task updates.
- Never rely on Vite's automatic port fallback. All repository Vite commands use `strictPort` so a collision fails visibly instead of opening a different worktree under an unexpected URL.
- The shared preview only contains committed changes merged into the integration branch. Uncommitted task changes remain isolated by design and must not be presented as globally available.
- Diagnose preview identity through `http://127.0.0.1:5175/__catsco_preview`; its `root` must match the primary integration worktree before reviewing shared output.

Before completion run:

```bash
pnpm test
pnpm run typecheck
pnpm run build
git diff --check
```

Report modified files, verification results, screenshots reviewed, and any placeholder business or legal content still requiring owner confirmation.

Do not commit, push, publish or create a GitHub repository unless the user has explicitly authorized that action.
