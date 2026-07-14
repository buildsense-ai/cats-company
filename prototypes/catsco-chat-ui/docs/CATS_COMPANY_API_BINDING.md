# CatsCompanyApi Binding Notes

Date: 2026-07-07

The current UI still uses `LocalChatApi` through:

```js
const ChatApi = LocalChatApi;
```

`CatsCompanyApi` is now defined in `app/api.js` but is not active yet. It is the future adapter for the real `cats-company` backend.

## Current Local Adapter

Used by the prototype today:

- `createSession()` -> `/api/new`
- `renameSession()` -> `/api/rename/{id}`
- `deleteSession()` -> `/api/delete/{id}`
- `sendChat()` -> `/api/chat`
- `getHealth()` -> `/api/health`

## CatsCompany Adapter Coverage

Prepared methods:

- account: `getMe`, `updateMe`
- conversations: `getConversations`, `getMessages`, `sendMessage`
- friends: `getFriends`, `getPendingFriends`, `sendFriendRequest`, `acceptFriend`, `rejectFriend`, `removeFriend`
- groups: `getGroups`, `createGroup`, `getGroupInfo`, `updateGroup`, `inviteToGroup`, `leaveGroup`
- agents: `getAgents`, `openAgent`
- relay: `getRelayConfig`, `getRelayCommercial`, `createRelaySession`, `getRelayKey`, `getRelayUsage`
- devices: `getDevices`, `createDesktopConnectSession`, `createDeviceConnectorPairing`
- feedback: `submitFeedback`

## Activation Plan

1. Add a login panel and store the backend token with `CatsCompanyApi.setToken(token)`.
2. Configure the backend base URL with `CatsCompanyApi.setBaseUrl(url)`.
3. Replace `const ChatApi = LocalChatApi;` with a runtime selector.
4. Convert local session/message shapes to CatsCo topic/message shapes.
5. Add WebSocket adapter for `/v0/channels`.

Do not switch the active adapter until account login and message shape conversion are ready.
