# Native iOS push notifications

The native CatsCo app uses UserNotifications and APNs alert notifications. It does not depend on a running WebSocket or a background execution grant. The iOS application lives in https://github.com/buildsense-ai/catsco-ios.

## Server configuration

Configure the `test` / `prod` GitHub environments with `APNS_TEAM_ID`, `APNS_KEY_ID`, `APNS_BUNDLE_ID` (`cc.catsco.ios`) and `APNS_P8_BASE64` (base64 of a dedicated Apple Developer **APNs** P-256 key). An App Store Connect API key does not authorize APNs. The deploy workflow passes credentials over SSH stdin to `deploy/sync-apns-env.py`; it installs an owner-only key in the existing secrets volume and puts only its path (`APNS_KEY_FILE=/run/catsco-secrets/apns.p8`) in the deployment environment file. Never commit a private key, device token, session token or real registration payload.

The service is disabled when credentials are missing or invalid. Empty GitHub secrets explicitly disable the integration on the next deployment. Production and sandbox use Apple's fixed HTTP/2 endpoints. Alert delivery uses the configured bundle identifier, priority 10, a one-hour expiration and bounded retries for transport errors, 429 and 5xx. Invalid/removed device tokens are deleted by account + endpoint + registration generation; credential errors do not delete devices. Logs omit provider request URLs and device tokens.

## Registration API

All routes use JWT authentication. Enrollment and tests reuse the existing per-user rate limits:

- `GET /api/push/ios/config`: availability and configured bundle identifier.
- `POST /api/push/ios/registrations`: `token`, `environment` (`sandbox` / `production`), random `registration_id`, optional `show_previews` (default false).
- `DELETE /api/push/ios/registrations`: matching device/environment/registration. Revocation remains available when APNs delivery is disabled.
- `POST /api/push/ios/test`: current `registration_id`. HTTP 202 means Apple accepted the request; it does not prove banner delivery.

Native registrations reuse `PushSubscriptionStore` in an `apns://environment/bundle/token` namespace with an `apns-v1` marker. This preserves atomic endpoint transfer and the existing combined limit of ten devices per account without a database migration. Native endpoints never pass through Web Push, and the browser API rejects them. Native and browser turn deduplication use separate coordinator instances so a status-only iOS alert cannot consume a later browser message notification.

Message alerts follow the existing visible-message filters, bot turn settling and per-conversation mute preferences. Task statuses additionally produce waiting-for-confirmation alerts and completion/failure fallbacks when no final reply exists. Superseded runs and repeated status heartbeats do not create duplicate task alerts. Channel-managed group traffic remains excluded. Native foreground presentation suppresses the currently open conversation on that device, while other devices remain eligible.

## iOS lifecycle and privacy

The native app asks for permission from Settings → 消息通知. Enrollments refresh on login/restore/foreground and APNs token changes. Logout, account changes and disabling notifications retire the registration; failed revocations remain in device-only Keychain for a subsequent retry. Already accepted/queued notifications and revocation while the device has no network are necessarily best effort. Native notification routes validate the current account registration generation before opening an existing conversation. Cold-start notification taps wait for account restoration. Tokens and pending revocation credentials stay out of UserDefaults.

The default alert hides employee names and message excerpts. Users can opt into message previews; lock-screen presentation also follows iOS settings. No badge count is sent because the server does not currently maintain an authoritative cross-device unread badge. This integration provides alert notifications; remote ActivityKit updates and WidgetKit refresh pushes are separate capabilities.

## Validation

Run `go test ./server/...`, focused `go test -race ./server -run 'TestNative|Test.*Push|Test.*TaskStatus'`, and `python3 deploy/sync-apns-env_test.py`. The native repository checks Debug/Release builds, signed production APNs entitlements, notification-settings UI on iPhone/iPad, and existing Swift core tests.

Physical-device acceptance: install the matching TestFlight build; enable notification permission; send a test; background and lock the phone; exercise human/new agent replies, completed/failed work and waiting confirmation; tap a notification from cold launch; confirm muted topics, account changes and disabling notifications. Apple acceptance, simulator UI checks and successful signing cannot establish physical-device banner/sound delivery.
