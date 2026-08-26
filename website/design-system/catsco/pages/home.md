# CatsCo Home Page Override

This page override replaces the generic AI/chatbot recommendations in `MASTER.md`.
CatsCo is an AI employee for non-technical office workers, not a chat product or a developer tool.

## Direction

- Mood: professional, trustworthy, future-facing, friendly, calm.
- Page thesis: a user gives CatsCo a goal; CatsCo performs the work and returns a finished deliverable.
- Signature element: a live “task workbench” that moves from a work brief through visible execution steps to a completed report. It must never resemble a chat transcript.
- Layout: editorial centered hero inspired by the supplied reference: standalone brand mark, one wide headline, one-line explanation, and a single CTA. The task workbench begins after the first viewport.
- Visual chapters: use a continuous neutral-dark product narrative from the header and hero through the real workbench demo (`#111111` / `#181818` / `#202020`), with neutral white and gray typography and green reserved for brand and selected states. Return to the light canvas for company purpose and team content, then finish on the green footer.
- Lower-page sequence: the One AI employee transition, four product preview videos, one real workbench demo, then the company-purpose and team sections. Do not add a separate example-deliverables section after the workbench demo.
- Present the real workbench demo as an interactive usage explorer: compact work-type tabs switch the example goal, reference files, stage accent, and concise outcome description while the approved workbench screenshot remains the visual anchor. Keep all examples explicitly labeled and use CatsCo colors and language rather than copying a reference brand.
- Insert a two-stage, sticky “为什么做 CatsCo” company-purpose page before the team portraits. The title first holds at the center of the viewport, then moves to the left as three short paragraphs beginning with “我们希望” appear in sequence. Reduced-motion mode shows the complete final state immediately.
- Product preview videos already explain configuration, execution visibility, connected environments, and authorization. Do not repeat those topics in additional lower-page feature sections.
- Example deliverables must be clearly labeled as examples and must not imply real customer data, measured performance, or downloadable production files.
- The fourth workflow preview is reserved for the cloud handoff story: `工作成果，统一留在云端`. Keep the copy focused on user access to completed work, without inventing storage, collaboration, or availability claims beyond the product's actual behavior.

## Tokens

| Role | Value |
| --- | --- |
| Primary | `#1A9D7A` |
| Primary dark | `#11745B` |
| Ink | `#15231F` |
| Secondary ink | `#53605C` |
| Canvas | `#FFFFFF` |
| Soft canvas | `#F5F8F6` |
| Mint surface | `#EAF7F2` |
| Warm surface | `#FFF8EA` |
| Border | `#DDE6E2` |

- Display/body: Inter with Noto Sans SC for Chinese glyphs.
- Radius: 12–20px; avoid pill-shaped containers except compact status labels.
- Shadows: broad and subtle, tinted toward green-gray; no glow.

## Motion

- One choreographed task-state sequence in the hero.
- Short scroll reveals and connector-draw transitions only.
- Use 240–500ms ease-out transitions.
- Respect `prefers-reduced-motion`; show the completed state immediately.

## Guardrails

- No purple AI palette, dark cinema theme, glass-heavy surfaces, particles, robots, brains, 3D scenes, or chat bubbles.
- Do not lead with Agent, Runtime, RPC, LLM, Connector, or Workflow.
- Developer links remain quiet and isolated in the footer.

## Approved Home Copy

- Headline: `CatsCo 你的专业AI员工`
- Lead: `一个可以进入用户授权工作环境，帮助用户完成真实任务的 AI 员工。`
- Header navigation: `企业` / `解决方案` / `定价`
- Header action: `登录` → `/login`
- Hero action: `立刻开始`
- Brand mark: use the supplied `/public/catsco-logo.png` asset globally; never substitute a generated icon.
