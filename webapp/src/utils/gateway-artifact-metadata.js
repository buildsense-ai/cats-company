// Gateway-only apps have no versioned artifact record. Keep this compatibility
// lookup bounded independently of launch, including a stalled JSON response body.
export const GATEWAY_METADATA_TIMEOUT_MS = 2500;

export async function loadGatewayArtifactMetadata(api, agentUid, appId) {
  const controller = new AbortController();
  let timer;
  const deadline = new Promise((resolve) => {
    timer = setTimeout(() => { controller.abort(); resolve(null); }, GATEWAY_METADATA_TIMEOUT_MS);
  });
  const lookup = Promise.resolve().then(() => api.getCloudArtifacts(agentUid, 'active', {
    signal: controller.signal,
    timeoutMs: GATEWAY_METADATA_TIMEOUT_MS,
    waitForMediaSession: false,
  })).then((registry) => (Array.isArray(registry?.artifacts) ? registry.artifacts : [])
    .find((item) => String(item?.id || '') === String(appId || '')) || null)
    .catch(() => null);
  try {
    return await Promise.race([lookup, deadline]);
  } finally {
    clearTimeout(timer);
  }
}
