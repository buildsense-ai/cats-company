// Public gateway/index fields observed on 2026-10-02; see
// docs/gateway-artifact-task-compatibility.md for sources and verification limits.
// This application exists in BOTH registries. A gateway-only app is not assumed
// to have a platform version. status/agent_uid are platform API enrichment.
export const promoGatewayApp = {
  id: 'promo-content-studio',
  title: '宣传内容产出应用',
  url: 'https://artifact.catsco.cc/promo-content-studio/',
  status: 'online',
};

export const promoRegistryArtifact = {
  id: 'promo-content-studio',
  title: '宣传内容产出应用',
  kind: 'html',
  url: 'https://agent-1071.artifacts.catsco.fun:19991/artifacts/promo-content-studio/latest/',
  publish_version: 18,
  agent_uid: '1071',
  status: 'active',
};
