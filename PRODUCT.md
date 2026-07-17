# Product

## Register

product

## Users

CatsCo users who have connected a local XiaoBa desktop assistant. They may use Codex, Pi, or both, and should not need to understand environment variables, CLI syntax, cursor state, or Runtime storage to control which local conversation history XiaoBa learns from.

## Product Purpose

CatsCo is the trusted control surface for a user's connected local assistant. For external history, users choose providers and a history window, preview the bounded impact, explicitly approve an import, and monitor or resume it from the WebApp. Success means provider setup and recovery are understandable without a terminal while local Runtime ownership, privacy, and safety remain explicit.

## Brand Personality

Friendly, restrained, trustworthy. The interface should feel calm and direct, with enough operational detail to support informed decisions without exposing implementation machinery.

## Anti-references

Do not resemble a developer console, raw environment editor, marketing dashboard, or card-heavy setup wizard. Do not hide consequential writes behind a casual toggle, expose transcript content in previews, force users to copy operation IDs, or imply that a remote WebApp directly owns local data.

## Design Principles

1. Put user intent first: ask which providers and how much history, not which flags to pass.
2. Preview before writing: show bounded counts and limits before any local history is admitted.
3. Make local authority visible: distinguish WebApp requests, connected-device execution, and offline or busy states.
4. Preserve progress: imports are resumable and status is durable, without asking users to manage operation identifiers.
5. Keep routine use quiet: after initial history import, continuous future-only learning runs with the connector and requires attention only when blocked.

## Accessibility & Inclusion

Meet WCAG 2.1 AA for contrast, focus visibility, labels, and keyboard operation. Never encode provider or import state by color alone. Respect reduced-motion preferences and keep status language understandable without specialist vocabulary.
