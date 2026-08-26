# CatsCo Pricing Page Override

This page implements the approved direction in `CatsCo_XiaoBa双轨定价与套餐表达_正式稿.pdf` while preserving the global CatsCo design and accessibility rules.

## Page Job

Explain that customers pay for CatsCo's sustained execution capability rather than chat volume, help an individual choose among Free, Pro, and Max, and clearly separate the scoped Business Start service from advanced enterprise work.

## Commercial Structure

- Public consumer plans are CatsCo Free at `¥0`, CatsCo Pro at `¥399/month`, and CatsCo Max at `¥799/month`.
- Card labels, capability copy, FAQ answers, and the pricing-to-login selection summary use the CatsCo plan names consistently.
- Free is a pre-launch basic experience entry. Do not publish a numeric quota or availability promise until its operating scope is approved.
- Do not add annual discounts, token counts, chat counts, or an unlimited-use promise without a new approved commercial decision.
- Public FAQ copy explains task capacity in user-facing language and does not expose internal token, cache, model-routing, or cost-control terminology.
- Until commercial activation is confirmed, the FAQ and release note state that this is a pre-release preview, formal purchase and upgrade are unavailable, and the opening date remains unconfirmed.
- Max's comparison point is approximately five times the task capacity of Pro, not five times raw top-model tokens.
- Pro lists only its own core capability set. Each plan uses the plain label `包含`; core and inherited functions use smaller gray line checks, while Max-only additions use white checks on green circles. All three cards use the same relaxed vertical rhythm for feature rows.
- Capacity cues are presented as plain title and supporting text without an icon, border, or tinted background.
- Max may use dark and muted brand green for its capacity title and supporting text; other card typography remains neutral.
- Consumer cards do not repeat billing or pre-launch notes directly below the price; those caveats remain in the dedicated page notice and FAQ so the capacity text and CTA can sit closer to the price.
- The plain `包含` label has no icon and aligns directly with the feature check column below it.
- Business Start is `¥4,999/month and up` for a scoped initialization service. Advanced transformation, integrations, private deployment, security governance, resident support, and large-scale Skill work require a separate consultation and scoped service confirmation.
- Business Start uses a compact split layout: the left side uses the CatsCo primary green with high-contrast white content and a white CTA with green text, ordered as plan name, explanation, price, and action. The right side lists the base scope and uses green rather than gold or blue accents. Keep the gap below consumer cards modest.
- The page-specific accent is `#45B19B`. Existing green text, action outlines, checks, and color panels should stay within this hue family: use the exact accent for solid roles, `#2D7A6A` where darker text or interaction contrast is needed, and translucent accent variants for quiet action borders and surfaces. Structural borders inside the comparison table are hue-neutral gray (`#D6D6D6`), while its action buttons retain the green accent. Do not retain an extra dark divider between the Business Start columns. The white Business Start CTA remains unchanged against its colored panel.
- Invitation codes manage test access and service capacity. They are not coupons and do not change the public price anchor.

## Direction

- Mood: transparent, calm, professional, selective, and outcome-oriented.
- Page background follows the updated homepage with a neutral graphite `#171717` canvas. Pricing surfaces use `#202020` and `#242424`, primary text uses `#F4F4F2`, secondary text uses neutral gray, and structural borders use hue-neutral dark gray. Do not use gradients, glow, glass effects, or green-tinted dark surfaces.
- Heading typography uses restrained medium-light weights: major headings around `540`, plan names and compact headings around `560`, and comparison/FAQ display headings around `450`. Price figures and action labels retain their existing emphasis.
- Structure: compact pricing title and explanatory copy, a three-card consumer comparison, Business Start placed directly after the consumer plans, invitation access, an explicit pre-launch notice, a detailed plan comparison, and a final FAQ accordion. Fair-use boundaries remain in the comparison notes and FAQ instead of a separate explanatory strip.
- Signature: Free and Pro use graphite card surfaces with the same hue-neutral `#929292` top edge accent, while Max uses a slightly raised graphite surface plus a green edge accent, capacity cue, and outlined `推荐` badge. The Business Start left panel keeps `#45B19B` with dark high-contrast text; surrounding content stays neutral graphite. CatsCo green remains the shared action and trust color rather than tinting every surface.
- Motion: consumer cards, the Business Start surface, and invitation-access card have no visible outer border at rest. Hover or keyboard focus within may reveal a restrained neutral or green border together with a slightly stronger shadow; surfaces do not translate, and no essential information depends on hover.

## Copy Rules

- Lead with sustained work: goal understanding, authorized execution, background progress, file delivery, and Skill accumulation.
- Use task capacity and work intensity instead of tokens, credits, model quotas, or chat counts.
- Fair-use language stays user-facing and avoids exposing internal scheduling, routing, cache, or token mechanics.
- Do not describe the Business Start price as an enterprise account fee or imply unlimited customization.
- Keep every service promise scoped and mark the page as a pre-launch proposal until commercial terms are confirmed.

## CTA Contracts

- Pro: `/login?plan=personal&billing=monthly&source=pricing`
- Max: `/login?plan=pro&billing=monthly&source=pricing`
- Free: `/login?plan=free&billing=monthly&source=pricing`
- Invitation access: `/login?access=invite&source=pricing`
- Business Start: `/contact?topic=enterprise&service=business-start&source=pricing`
- The login page validates the exact plan, billing, and invite values before displaying a selection.
- Pricing selection opens only the login prototype and never initiates payment.

## Responsive and Accessibility

- The 1200px page boundary aligns with the shared header controls at desktop width.
- Consumer cards use three equal columns on desktop and stack Free, Pro, then Max below tablet width.
- Keep the three consumer cards within the centered 1200px content boundary used by the shared Header controls on desktop, so the card edges align with the CatsCo logo and Download action. Use a taller proportion for easier vertical scanning. Compact the price and action area, then give the included and added feature groups enough room to remain visually distinct.
- The detailed comparison uses only approved price, capacity, and capability language. On narrow screens it remains inside a labeled horizontal scroll region rather than causing page-level overflow.
- The FAQ title is exactly `常见问题`. Use regular-weight questions, lighter explanatory answers, visible keyboard focus, thin standalone plus/minus indicators, and one control that toggles all answers between expanded and collapsed states.
- In the dark theme, FAQ plus/minus indicators are white. Comparison-table included states use a standalone `#45B19B` check without a circular background. The Max top rule and recommendation outline use the exact same `#45B19B` as its primary button.
- Open FAQ items keep the question row at the same fixed height as the collapsed state so the question text does not shift. The answer alone moves closer to the question while preserving clear separation before the next item.
- The FAQ section uses a pure white background against the neutral `#F8F8F8` page surface.
- Business Start becomes one column below tablet width.
- Preserve semantic headings, visible focus states, 48px action targets, readable fair-use copy, reduced-motion support, and a no-horizontal-scroll 390px layout.
