# Office Editor UX Fixes and Quiet Versioned Save

## 1. Delivery identity

- Branch: `feat/office-editor-ux-five-fixes`
- Implementation commit: `756690231dd9473a57f9ea4fb489f5a8a08f7b85`
- Commit subject: `feat(office-editor): isolate drafts and add quiet versioned saves`
- Base commit: `67ae2627`
- This report is stored in a subsequent docs-only commit because a commit cannot contain its own final hash.

## 2. U-01 to U-05

| Item | Status | Result |
|---|---|---|
| U-01 | PASS | PPT/Word open read-only; property inputs are disabled; PPT has no selection frame. |
| U-02 | PASS | Edit mode enables controls and shows the PPT selection; Complete returns to read-only with the draft still present. |
| U-03 | PASS | Width is clamped to 220–560px, survives reload, and the <=900px layout is single-column with no resizer. |
| U-04 | PASS | Local textarea echoes immediately, commits through rAF, survives selection/page switches, and flushes on blur/IME completion. |
| U-05 | PASS | Default save performs upload + binding and no sendMessage; the UI states that 小八 has not received the new version. Cross-device behavior is not claimed; see S-07. |

### U-01 browser evidence

Raw output:

```json
{"mode":"readonly","enabledSideInputs":0,"selection":null,"toggle":"编辑"}
```

The component suite also proves disabled fields and no `[data-office-selection]` in `webapp/src/office/ui-readonly.test.jsx`.

### U-02 browser evidence

Raw output:

```json
{"before":{"mode":"edit","enabledSideInputs":8,"hasSelection":true},"canvasHasDraft":true,"afterMode":"readonly","afterSelection":null,"afterValue":"E2E-DRAFT-SURVIVES","afterCanvasHasDraft":true}
```

### U-03 browser evidence

The desktop session had a persisted 400px panel. At 800px the body became one column and the resizer was hidden:

```json
{"desktop":{"viewport":1440,"grid":"233px 6px 400px","resizerDisplay":"block","panelWidth":"400px"},"narrow":{"viewport":800,"match":true,"grid":"756px","resizerDisplay":"none","officeVisible":true}}
```

Reopen persistence:

```json
{"stored":"400","restored":"400px","grid":"233px 6px 400px"}
```

`webapp/src/office/panel-width.test.js` passes the default, clamp, storage, and narrow-layout checks.

### U-04 browser evidence

```json
{"immediate":"U04-LOCAL-DRAFT","canvasUpdateMs":38,"afterElementSwitch":"U04-LOCAL-DRAFT","afterPageSwitch":"U04-LOCAL-DRAFT","canvasHasDraft":true}
```

Focused local textarea tests cover immediate echo, one-frame coalescing, blur flush, IME completion, external value adoption, and unmount flush.

### U-05 browser evidence

The real upload endpoint and the real message-send boundary were observed. Because the running backend is older than this commit and its binding route returns 404, the browser probe fulfilled POST `/api/files/version-bindings` at the HTTP boundary. This proves the frontend contract only; it does not prove a deployed cross-device save.

```json
{"cardsBefore":38,"cardsAfter":38,"toast":["已保存最新版，但尚未发给小八；会话里仍是旧附件"],"requests":[{"method":"GET","url":"http://localhost:5173/api/messages?topic_id=grp_7277&limit=50&offset=0&latest=1"},{"method":"GET","url":"http://localhost:5173/api/messages?topic_id=grp_7277&limit=50&offset=0&latest=1"},{"method":"GET","url":"http://localhost:5173/api/files/version-bindings?topic_id=grp_7277&source_message_id=2122518&source_attachment_index=0"},{"method":"POST","url":"http://localhost:5173/api/upload?type=file&raw=1"},{"method":"POST","url":"http://localhost:5173/api/files/version-bindings"}],"responses":[{"method":"GET","url":"http://localhost:5173/api/messages?topic_id=grp_7277&limit=50&offset=0&latest=1","status":200},{"method":"GET","url":"http://localhost:5173/api/files/version-bindings?topic_id=grp_7277&source_message_id=2122518&source_attachment_index=0","status":404},{"method":"POST","url":"http://localhost:5173/api/upload?type=file&raw=1","status":200},{"method":"POST","url":"http://localhost:5173/api/files/version-bindings","status":200}]}
```

There is no POST `/api/messages` in that request list. Message card count stayed 38 -> 38.

## 3. S-01 to S-07

| Item | Status | Result |
|---|---|---|
| S-01 | PASS | Before/after quiet save: old attachment URL unchanged, conversation cards unchanged, search result IDs unchanged. |
| S-02 | PASS | Explicit sync produced exactly one file message, with the latest upload as its file payload. |
| S-03 | PASS | Handler GET returns the stored latest pointer by source triple; live deployment currently returns 404, so no deployed cross-device claim is made. |
| S-04 | PASS | Server-first/local-fallback resolution preserves the original when no pointer exists and does not fabricate one. |
| S-05 | PASS | Server rejects source mismatch, unauthorized writes, non-Office URLs, and unavailable store; valid upsert succeeds. |
| S-06 | PASS | Unsynced versions show no annotation UI; after explicit sync the annotation source is `{message_id: 2143885, attachment_index: 0}`. |
| S-07 | NOT RUN | The real T-26 five-step chain needs a current backend and a live database; neither is available in this environment. |

### S-01 raw evidence

```json
{"searchBefore":{"status":200,"ids":[2143885,2123016,2123000,2122993,2122992,2122989,2122985,2122971,2122970,2122969,2122943,2122942,2122941,2122940,2122939,2122938,2122880,2122868,2122865,2122855],"count":20},"searchAfter":{"status":200,"ids":[2143885,2123016,2123000,2122993,2122992,2122989,2122985,2122971,2122970,2122969,2122943,2122942,2122941,2122940,2122939,2122938,2122880,2122868,2122865,2122855],"count":20},"searchIDsUnchanged":true,"cardsBefore":38,"cardsAfter":38,"oldHrefBefore":"/uploads/files/20261115_fe53276aab94834563013ce5999e7cb3.pptx?download=1&name=sample.pptx","oldHrefAfter":"/uploads/files/20261115_fe53276aab94834563013ce5999e7cb3.pptx?download=1&name=sample.pptx","oldOriginalUnchanged":true,"toast":["已保存最新版，但尚未发给小八；会话里仍是旧附件"]}
```

### S-02 raw evidence

```json
{"annotationEditors":1,"source":{"topic_id":"grp_7277","message_id":2143885,"attachment_index":0},"notice":"已同步给小八。会话里出现的新附件就是这一版，小八读到的也是这一版","cardsBefore":37,"cardsAfter":38,"oldOriginalUnchanged":true}
```

The component test asserts one `api.sendMessage` call and that its file block payload equals the latest upload.

### S-03 raw Go evidence

```text
=== RUN   TestFileVersionBindingValidUpsert
--- PASS: TestFileVersionBindingValidUpsert (0.01s)
=== RUN   TestFileVersionBindingGETHit
--- PASS: TestFileVersionBindingGETHit (0.00s)
=== RUN   TestFileVersionBindingRejectsSourceMismatch
--- PASS: TestFileVersionBindingRejectsSourceMismatch (0.00s)
=== RUN   TestFileVersionBindingRejectsUnauthorized
--- PASS: TestFileVersionBindingRejectsUnauthorized (0.00s)
=== RUN   TestFileVersionBindingRejectsNonOfficeURL
--- PASS: TestFileVersionBindingRejectsNonOfficeURL (0.01s)
=== RUN   TestFileVersionBindingStoreUnavailable
--- PASS: TestFileVersionBindingStoreUnavailable (0.00s)
PASS
ok  	github.com/openchat/openchat/server	0.427s
GO_HANDLER_EXIT=0
```

Live runner check:

```text
VERSION_BINDING_GET_STATUS=404
```

The 404 is why S-07 remains NOT RUN and why U-05 does not claim deployed cross-device behavior.

### S-04 raw evidence

```text
Test Files  1 passed (1)
Tests       11 passed (11)
VITEST_VERSION_BINDING_EXIT=0
```

Coverage includes server-first resolution, local fallback on outage/404, original-file fallback, request construction, local-only save, and no fabricated binding.

### S-05 raw evidence

The six handler tests above cover valid upsert, GET hit, source mismatch, unauthorized access, non-Office URL, and unavailable store. No test was skipped or weakened.

### S-06 raw evidence

Unsynced:

```json
{"annotationEditors":0,"pageLabel":"1 / 4","syncDisabled":false,"previewText":"sample-inserted.pptx ..."}
```

Synced:

```json
{"annotationEditors":1,"source":{"topic_id":"grp_7277","message_id":2143885,"attachment_index":0},"notice":"已同步给小八。会话里出现的新附件就是这一版，小八读到的也是这一版","cardsBefore":37,"cardsAfter":38,"oldOriginalUnchanged":true}
```

The FilePreviewPanel regression suite also passes:

```text
Test Files  1 passed (1)
Tests       124 passed (124)
VITEST_FILE_PREVIEW_EXIT=0
```

## 4. L0 frontend tests

Office suite:

```text
Test Files  12 passed (12)
      Tests  196 passed (196)
VITEST_OFFICE_EXIT=0
```

Messages/API suite:

```text
Test Files  2 passed (2)
      Tests  436 passed (436)
VITEST_MESSAGES_API_EXIT=0
```

Combined: `632 passed`, `0 failed` (196 + 436). Both groups exceed the brief's 178 and 434 thresholds; the combined total exceeds 612.

Supporting focused suite:

```text
Test Files  5 passed (5)
      Tests  42 passed (42)
VITEST_FOCUSED_EXIT=0
```

## 5. Build

```powershell
& 'C:\Program Files\nodejs\node.exe' .\node_modules\vite\bin\vite.js build
```

```text
✓ built in 855ms
...
precache 9 entries (351.30 KiB)
files generated
  build/sw.js
VITE_BUILD_EXIT=0
```

The chunk-size warning is non-fatal; exit code is 0.

## 6. Go handler and schema tests

Handler result is in S-03: `6/6 PASS`, `GO_HANDLER_EXIT=0`.

Schema result:

```text
=== RUN   TestFileVersionBindingsSchemaColumnsUniqueAndIndex
--- PASS: TestFileVersionBindingsSchemaColumnsUniqueAndIndex (0.00s)
PASS
ok  	github.com/openchat/openchat/server/db/mysql	0.170s
=== RUN   TestFileVersionBindingsSchemaColumnsUniqueAndIndex
--- PASS: TestFileVersionBindingsSchemaColumnsUniqueAndIndex (0.00s)
=== RUN   TestFileVersionBindingsSchemaMigrationUpDown
--- PASS: TestFileVersionBindingsSchemaMigrationUpDown (0.00s)
PASS
ok  	github.com/openchat/openchat/server/db/postgres	0.218s
GO_SCHEMA_EXIT=0
```

The final runs used `GOPROXY=https://goproxy.cn,direct`. No dependency timeout remained in the final evidence.

## 7. PostgreSQL migration

```text
Name                                  Length
----                                  ------
000067_file_version_bindings.down.sql     44
000067_file_version_bindings.up.sql      925
MIGRATION_COUNT=2
UP_HAS_CREATE_TABLE=True
DOWN_DROPS_SAME_TABLE=True
DOWN_TOUCHES_MESSAGES=False
```

`TestFileVersionBindingsSchemaMigrationUpDown` also passes.

## 8. Git status and staged stat

Before the implementation commit:

```text
M  server/cmd/server.go
A  server/db/migrations/postgres/000067_file_version_bindings.down.sql
A  server/db/migrations/postgres/000067_file_version_bindings.up.sql
A  server/db/mysql/file_versions.go
A  server/db/mysql/file_versions_schema_test.go
M  server/db/mysql/schema.go
A  server/db/postgres/file_versions.go
A  server/db/postgres/file_versions_schema_test.go
M  server/db/postgres/schema.go
A  server/file_versions.go
A  server/file_versions_test.go
A  server/store/file_versions.go
M  webapp/src/api.js
M  webapp/src/api.test.js
M  webapp/src/office/canvas.test.jsx
M  webapp/src/office/docx/render.jsx
A  webapp/src/office/local-draft.test.jsx
A  webapp/src/office/panel-width.test.js
M  webapp/src/office/save.js
M  webapp/src/office/save.test.js
A  webapp/src/office/ui-readonly.test.jsx
M  webapp/src/office/ui/EditableCanvas.jsx
A  webapp/src/office/ui/LocalDraftTextarea.jsx
M  webapp/src/office/ui/OfficePreview.jsx
M  webapp/src/office/ui/PropertyPanel.jsx
M  webapp/src/office/ui/WordPreview.jsx
M  webapp/src/office/ui/draft-render.js
A  webapp/src/office/ui/office-panel-width.js
M  webapp/src/office/ui/word-draft-render.js
A  webapp/src/office/version-binding.js
A  webapp/src/office/version-binding.test.js
M  webapp/src/styles/office-preview.css
M  webapp/src/views/messages-view.jsx
M  webapp/src/views/messages-view.test.jsx
M  webapp/src/widgets/chat-message.jsx
M  webapp/src/widgets/chat-message.test.jsx
?? docs/office-editor/IMPL-PROMPT.md
?? docs/office-editor/UX-FIX-PLAN.md
```

Before the implementation commit:

```text
 36 files changed, 2706 insertions(+), 395 deletions(-)
```

No `TEST-PLAN.md`, acceptance sample, browser profile, log, build output, credential, `node_modules`, or prompt/plan document was staged. The implementation commit contains 36 files.

## 9. NOT RUN and remaining environment work

### S-07 / T-26 — NOT RUN

Reason: a real five-step T-26 run needs a current Go server and a reachable MySQL/PostgreSQL database. This environment has neither:

```text
NO_DB_PORT_3306_OR_5432
DOCKER_ERROR=The term 'docker' is not recognized as the name of a cmdlet, function, script file, or operable program.
VERSION_BINDING_GET_STATUS=404
```

The running backend is older than this implementation and does not expose `/api/files/version-bindings`. A route-level binding response was used only to verify U-05 frontend behavior; it is not counted as a cross-device or T-26 pass.

To complete S-07/T-26:

1. Start MySQL or PostgreSQL, apply the schema including `000067_file_version_bindings`, or run the repository's normal database bootstrap.
2. Configure `OC_DB_DRIVER`/DSN and other required server environment variables, rebuild and restart the Go server from this commit.
3. Confirm GET `/api/files/version-bindings?topic_id=...&source_message_id=...&source_attachment_index=...` returns the latest pointer, not 404.
4. Start Vite with `VITE_BACKEND_TARGET` pointing at that server, and sign in to a conversation containing a real PPTX attachment.
5. Execute T-26 steps 1–5 through the explicit “同步给小八” action: edit, quiet save, verify no implicit message, explicitly sync one message, reopen the new attachment, then verify 小八 reads the synced version.

No other acceptance item is NOT RUN. No acceptance criterion or assertion was deleted, skipped, or relaxed.