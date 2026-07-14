# CatsCo Replacement Gap List

Date: 2026-07-07

Goal: turn the current Mini Chat frontend into a UX replacement candidate for CatsCo Web / `cats-company`.

## Current Project Status

The current frontend is strongest as a polished interaction prototype for:

- AI chat
- session list
- pinned conversations
- local rename/delete/share actions
- collapsed/expanded sidebar
- groups and friends as UI concepts
- input bar with upload affordance
- generation stop button
- generation process display
- light/dark theme
- compact desktop layout

It does not yet fully replace CatsCo because most social, account, realtime, and backend integration features are still local-only or absent.

## Replacement Scope

### Must Have For First Replacement Demo

These are the features needed for a credible internal demo:

- Account entry point and profile panel
- Real session list
- Real message history
- AI chat streaming
- Stop generation
- Rename/delete/pin/share conversation
- Group list
- Friend list
- Create group
- Add friend
- File/image upload affordance
- Settings menu
- Feedback entry
- Stable light/dark theme
- Responsive desktop layout

Current status: partially complete.

### Must Have For Real CatsCo Replacement

These need backend integration:

- Login/register/session token
- User profile from backend
- Friends from backend
- Friend request accept/reject/block/remove
- Groups from backend
- Group create/update/invite/leave/disband
- Message send through REST or WebSocket
- Message history pagination
- Online status
- File upload through backend
- WebSocket reconnect and missed-message recovery
- Bot/Agent opening from backend roster
- Device/desktop connector entry
- Feedback submission

Current status: mostly not connected.

### Can Wait

These are important for the full CatsCo product, but should not block the UX replacement prototype:

- Relay commercial package UI
- Relay key management
- Detailed usage dashboard
- Feishu channel binding
- Weixin channel binding
- Admin account center
- Tutorial task admin
- Bot deployment console
- Multi-instance Redis runtime mode

Current status: postpone.

## API Mapping Target

The future frontend adapter should be shaped around these CatsCo-style endpoints:

- Auth: `/api/auth/login`, `/api/auth/register`, `/api/auth/send-code`
- User: `/api/me`, `/api/me/update`
- Conversations: `/api/conversations`
- Messages: `/api/messages`, `/api/messages/send`
- Friends: `/api/friends`, `/api/friends/request`, `/api/friends/accept`, `/api/friends/reject`, `/api/friends/remove`
- Groups: `/api/groups`, `/api/groups/create`, `/api/groups/info`, `/api/groups/update`, `/api/groups/invite`, `/api/groups/leave`
- Agents: `/api/agents`, `/api/agents/open`
- Uploads: `/api/upload`
- Feedback: `/api/feedback`
- WebSocket: `/v0/channels`

The current local backend can stay as a development adapter while this shape is built.

## Recommended Milestones

### Milestone 1: Prototype Lock

- Stop large visual experiments.
- Stabilize current layout and interaction rules.
- Fix obvious broken states.
- Keep using local `server.py`.

### Milestone 2: Adapter Boundary

- Define `ChatApi` methods.
- Route all fetch calls through that API layer.
- Keep the current backend as `LocalChatApi`.
- Prepare a `CatsCompanyApi` with the real endpoint names.

### Milestone 3: Data Model Alignment

- Normalize local data to match CatsCo concepts:
  - user
  - session/conversation
  - topic
  - message
  - group
  - friend
  - agent

### Milestone 4: Production Frontend Shell

- Move from single-file prototype to component-based frontend.
- React is the easiest match because `cats-company/webapp` already uses React.
- Keep the current prototype as the visual reference.

## Product Direction

The new frontend should not copy CatsCo visually. It should replace it by being:

- calmer
- easier to scan
- less visually noisy
- more predictable
- more desktop-tool oriented
- clearer about running/generating/stopped states
- better organized around conversations, people, groups, and AI agents
