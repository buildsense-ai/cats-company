# Test-only screenshot fixture

Canonical request/schema: [Gateway artifact annotations](../../docs/gateway-artifact-annotations.md); server validation: [artifact_annotation_images.go](../artifact_annotation_images.go).

Run actual Go auth/upload/bound-message/history handlers and optionally the real XiaoBa image consumer:

```sh
CATSCO_SCREENSHOT_EXPORT_DIR=/tmp/catsco-screenshot-server-actual-fixture \
XIAOBA_ROOT=/path/to/XiaoBa-CLI \
go test ./server -run '^TestArtifactScreenshotOwnedUploadTwoImagesPersistDeliveryHistoryAndDedup$' -count=1 -v
```

The export directory contains actual uploaded JPEG full/crop files at `uploads/images/<key>.jpg`, actual `/api/messages?agent_context=1` response `agent-history.json`, and actual live/WS replay envelopes `delivery.json`. Re-running the test appends new random keys; only the two keys referenced by the exported `agent-history.json`/`delivery.json` belong to that export, so regenerate rather than reusing an older directory. Export is test-generated, not browser renderer pixels. A reviewer may serve this directory only on loopback and consume history/images without JWT. Source upload serving is existing public random-key storage. Never follow redirects or use arbitrary domains from blocks.

`xiaoba-screenshot-vision-runner.cjs` reads the actual TypeScript AST from the supplied checkout (no source changes), runs actual parseMessage + HTTP downloadFile + buildMultimodalMessage + createImageBlock/sharp, asserting two real JPEG base64 model input blocks for live and WS replay. Cloud session restore is implemented in XiaoBa-CLI and requires its own validation; this runner covers live and replay input. No provider/network model request occurs.

For browser integration, the existing `TestArtifactOpenBindingDevHTTPFixture` now includes actual authenticated `/api/upload?type=image` and `/uploads/images/...` serving and metadata-aware durable test store/agent-context GET. It accepts the same documented environment variables in `docs/gateway-open-binding-server.md`; generated credentials are written only to a caller-chosen `/tmp` JSON mode0600. No production route or fixture backdoor is installed.

Screenshot proof is a private mode0600 local sidecar outside public upload directories, binding authenticated uploader/size/hash. `file_key` references the proof; no extra bearer proof is passed to apps. Existing/anonymous/mobile files without a receipt fail closed when used as screenshots. Storage must be shared together with images across replicas; unlike open_ref it survives handler recreation. Cleanup must remove the matching receipt when an image is removed; current upload service has no image retention/delete mechanism.

`content_blocks` may omit images for an explicit text-only send. With images it must contain exactly full/crop `image` blocks, plus optional text blocks. Canonical image payload is `{url:'/uploads/images/<key>.jpg',file_key,name:'gateway-full.jpg'|'gateway-crop.jpg',mime_type:'image/jpeg',size,width,height,screenshot_role:'full'|'crop'}`. JPEG bytes are decoded, SHA256/size checked, dimensions<=2048 and <=2MiB each (<=4MiB pair); duplicate key/role rejected. Canonical annotation JSON never contains screenshot bytes, open_ref or auth.
