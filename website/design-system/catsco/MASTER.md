# CatsCo Website Design System

> Page-specific files under `design-system/catsco/pages/` may specialize this system, but they must preserve the brand, accessibility and content rules below.

**Product:** CatsCo public marketing website
**Audience:** Non-technical office users, team leaders and enterprise decision-makers
**Core promise:** Give CatsCo a goal, authorize the relevant work environment, and receive a finished deliverable.
**Updated:** 2026-08-06

## Brand direction

- Mood: professional, trustworthy, future-facing, friendly and calm.
- CatsCo should feel like a capable employee entering a real workflow, not a chatbot waiting for prompts.
- The visual signature is visible work progression: goal, authorized execution, progress and finished result.
- Use one meaningful visual or motion idea per page. Keep the surrounding interface quiet and precise.
- Brand spelling is always `CatsCo`.

## Color system

| Role | Value | CSS token |
| --- | --- | --- |
| Primary | `#1A9D7A` | `--cats-primary` |
| Primary hover | `#148363` | `--cats-primary-hover` |
| Primary dark | `#11745B` | `--cats-primary-dark` |
| Ink | `#15231F` | `--cats-ink` |
| Secondary ink | `#52605B` | `--cats-text-secondary` |
| Muted text | `#66736E` | `--cats-muted` |
| Canvas | `#F8F8F8` | `--cats-canvas` |
| Surface | `#FFFFFF` | `--cats-surface` |
| Soft mint | `#EAF7F2` | `--cats-mint-surface` |
| Warm surface | `#FFF8EA` | `--cats-warm-surface` |
| Border | `#DDE6E2` | `--cats-border` |
| Strong border | `#B9CDC5` | `--cats-border-strong` |
| Danger | `#B42318` | `--cats-danger` |
| Focus ring | `rgba(26, 157, 122, 0.35)` | `--cats-focus-ring` |

Green is the primary action and trust color. Blue, violet, amber and pink may identify distinct capability types inside illustrations, but never replace the green brand system.

## Typography

- Latin: Inter.
- Chinese: Noto Sans SC, then PingFang SC and Microsoft YaHei.
- Body copy starts at 16px where space permits, with 1.55–1.75 line height.
- Chinese display headings use a calm medium weight with slightly open tracking; section and card headings follow the same hierarchy across pages.
- Latin-only display headings may use restrained negative tracking, while mixed Chinese and Latin headings should not be compressed.
- Use plain, active Chinese. Explain what the user controls and receives.
- Avoid internal terms such as Agent, Runtime, RPC, LLM, tokens and compute units in public marketing copy.

## Layout and density

- Marketing pages are spacious: 20px mobile gutters, 32px tablet gutters and a 1200px desktop content ceiling.
- Standard section rhythm: 88–132px desktop and 64–88px mobile.
- Radius scale: 8px controls, 12px compact cards, 18–24px feature surfaces.
- Shadows are broad, subtle and green-gray tinted. Do not use glow.
- Do not use pill containers except compact statuses, billing controls and recommendation labels.

## Shared components

### Primary action

- Minimum height 48px.
- Green fill, white text, visible focus ring.
- Hover changes color or border without moving surrounding layout.
- Labels describe the result: “登录 CatsCo”, “联系企业顾问”, “获取发布通知”.

### Secondary action

- Transparent or white surface with a restrained border.
- Equal interaction and focus quality to the primary action.
- The two final-footer actions intentionally share the same default, hover, active, and focus appearance even though they keep different destinations.

### Cards

- White or lightly tinted surfaces with a 1px semantic border.
- Hover may strengthen the border or shadow; no card translation on pricing and comparison surfaces.
- Card hierarchy comes from copy and spacing, not decorative gradients.

### Forms

- Every control has a visible label, meaningful name, autocomplete and inline feedback.
- Minimum control height 48px.
- Focus uses the CatsCo ring; never remove outline without replacement.
- Prototype-only forms must state clearly that data is not sent.

## Motion

- Default interaction duration: 160–280ms.
- Page reveals: 240–500ms ease-out.
- Animate opacity and transform where possible.
- The homepage may use one choreographed work-progress demonstration.
- Enterprise, pricing, legal and contact pages use restrained motion only.
- `prefers-reduced-motion` must reveal the final state without requiring scroll or pointer movement.

## Accessibility and responsive floor

- Maintain readable contrast and visible keyboard focus.
- Provide a skip link and semantic heading order.
- Navigation must remain available on mobile.
- Test 390px, 768px, 1024px and 1440px widths.
- No horizontal page scrolling.
- Images include dimensions; decorative images use empty alt text.
- Stateful controls expose selected, expanded and status information.

## Content guardrails

- Do not invent customer logos, compliance certifications, deployment availability, response times or performance numbers.
- Enterprise claims must be framed as product direction or confirmed service scope, not assumed certification.
- Pricing language stays explicit about billing and cancellation.
- Legal pages remain marked as pre-launch drafts until reviewed against the real operating entity, jurisdictions and data flows.
- Team names and portraits require publication approval.

## Forbidden patterns

- Generic AI purple as the primary palette.
- Dark cinema themes, neon glow, heavy glassmorphism or decorative particles.
- Robots, brains and chat bubbles as the central brand metaphor.
- `transition: all`.
- Invisible focus states.
- Placeholder-only form labels.
- No-op buttons or text styled as links.
- Page-specific CSS placed in another page's stylesheet.

## Required verification

Before merging a page task:

1. Run `pnpm test`.
2. Run `pnpm run typecheck`.
3. Run `pnpm run build`.
4. Run `git diff --check`.
5. Review desktop and 390px mobile screenshots.
6. Confirm all visible actions have a real destination or an honest unavailable-state explanation.
