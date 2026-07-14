# Mini Chat UI System

Date: 2026-07-07

This document is the styling baseline for the current frontend and the future CatsCo replacement frontend.

## Design Intent

The interface should feel like a daily-use desktop chat tool:

- calm
- clear
- compact
- predictable
- not marketing-like
- not visually flashy
- good for repeated work

## Core Layout

### Sidebar

- Expanded width: keep around 250 px.
- Collapsed width: keep icon-only and stable.
- Rows should share the same visual grid:
  - left icon slot: 16 px
  - right action slot: 28 px
  - row horizontal padding: 10 px
  - row height: about 38 px
- Group, friend, and AI conversation headers should feel like the same component tier.
- Collapsed mode should prioritize quick conversation switching.

### Header

- The active conversation title should be visually centered.
- Model status stays on the left.
- Theme toggle stays on the right.
- Header controls should not compete with message content.

### Message Area

- Message cards should use consistent radius and padding.
- User, assistant, and error states should differ clearly but quietly.
- Message actions should stay out of the content flow and not overlap process panels.
- Regenerate should live near the lower-right area of assistant messages, with minimal background.

### Input Bar

- The input bar is the primary action area.
- The send button uses brand green.
- While generating, the send button becomes a red stop button.
- Stop should only be mouse-click driven, not keyboard-triggered accidentally.
- Upload/add button should be visually centered and not add extra colored noise.

## Tokens

### Radius

- `--ui-radius-xs`: 6 px
- `--ui-radius-sm`: 8 px
- `--ui-radius-md`: 10 px
- `--ui-radius-lg`: 14 px

Usage:

- Small buttons and inline controls: `xs` or `sm`
- Inputs and menus: `md`
- Message cards and large panels: `lg`

### Font Sizes

- Sidebar rows and controls: 13 px
- Body/message text: 15 px
- Hints/meta: 12 px
- Welcome headline: large, but only in the empty-state hero

Avoid viewport-scaled font sizes.

### Spacing

- Tiny gap: 6 px
- Small gap: 8 px
- Medium gap: 12 px
- Control horizontal padding: 12 px

Use spacing to show hierarchy before increasing font size.

### Color

Brand green:

- Base: `rgb(24, 133, 103)`
- Send button normal state should vary by brightness/opacity only.
- Do not shift hue for send button states.

Stop red:

- Generating stop button: clear red
- Hover/active may darken slightly
- It should remain obviously a stop state

Neutral hover:

- Use gray/white overlays.
- Avoid blue or green hover backgrounds unless the control is explicitly brand/action-related.

### Interaction States

Every interactive component should have:

- default
- hover
- active/selected
- disabled
- focus-visible where keyboard access matters

Components covered:

- buttons
- list rows
- inputs
- menus
- modals
- message action buttons
- toast
- sidebar section headers
- welcome suggestion pills

## Component Rules

### Buttons

- Icon buttons should use real icons or consistent SVGs.
- Text buttons are for clear commands only.
- Hover changes should be subtle.
- Disabled controls should look inactive but still readable.

### List Items

- Sidebar sessions, groups, and friends should share:
  - row height
  - icon slot
  - text size
  - hover background
  - active background
- Text brightness can show hierarchy; avoid too many font sizes.

### Menus

- Menu padding: 6 px container, 8-10 px item padding.
- Menu item height should match compact controls.
- Danger actions use red text, not a large red background.

### Message Cards

- Keep content readable first.
- Process panels should not overlap copy/share/action buttons.
- Assistant process details should expose status, not private chain-of-thought.

### Empty State Suggestions

- Keep to two rows.
- Text length can vary naturally.
- Hover should make text brighter and background slightly gray.
- Avoid blue-tinted gray in dark mode.

## Refactor Boundary

Before adding another large UI feature, create these modules or equivalent component sections:

- `api`
- `state`
- `sidebar`
- `messages`
- `input`
- `menus`
- `settings`
- `theme`

Until then, new CSS should go through the existing token layer instead of adding unrelated one-off selectors.
