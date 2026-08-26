# Opaque Artifact frame bridge

Managed same-origin Artifacts are rendered in an opaque sandbox (`null` origin)
so the page cannot access the CatsCo workspace. The parent must not send context
or result payloads through the `WindowProxy` channel.

## Contract

The bridge uses `catsco.artifact-frame-bridge.v1`:

1. Managed Artifact URLs are canonical HTTP(S) paths without a query or
   fragment. The parent adds a one-time `catsco_bridge_nonce` fragment
   parameter to the iframe URL. The URL helper still retains any unexpected
   fragment text before that parameter; a compliant runtime must remove only
   the reserved parameter (using `history.replaceState` or its router's
   equivalent) before exposing the fragment to the Artifact application.
2. The parent waits for the iframe's first `load` event and marks that document
   binding ready. A later `load` invalidates the binding and aborts in-flight
   requests; the parent does not reuse the old port.
3. The parent sends `catsco.artifact.frame-bridge.request.v1` with `*` and one
   transferred `MessagePort`. This envelope contains protocol metadata only;
   it must not contain page context or result data.
4. The Artifact runtime reads `catsco_bridge_nonce` from `location.hash` during
   its initial document load and replies on the transferred port with
   `catsco.artifact.frame-bridge.ready.v1`, including the exact nonce, contract
   version, and bridge ID. It must treat the nonce and transferred port as
   one-document capabilities and discard both on navigation.
5. Only after READY validation does the parent send the existing context/result
   request envelope over the port. Responses must use the same port and request
   ID.

The nonce is intentionally not included in the WindowProxy handshake. A page
that was navigated to before the handshake therefore cannot authenticate itself
unless it received and retained the original fragment. Artifact serving URLs
must return the managed document directly and must not redirect to an unrelated
document, because an opaque `WindowProxy` cannot prove the identity of a
redirected document by origin alone. The server validates the canonical URL
shape; deployment health checks must also exercise the published HTML URL with
redirects disabled. A runtime that cannot implement this contract is treated
as legacy:
preview remains available, page context falls back to an empty snapshot, and
result delivery receives `opaque_frame_bridge_required` without sending the
result payload.

Cross-origin Artifact frames continue to use exact-origin `postMessage` checks
and do not use this bridge.
