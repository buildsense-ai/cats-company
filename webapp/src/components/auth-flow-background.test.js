import {
  AUTH_FLOW_CYCLE,
  AUTH_FLOW_MESH,
  AUTH_FLOW_PARTICLE_COUNT,
  AUTH_FLOW_POINTER_RADIUS,
  authFlowProgress,
  authFlowSurfacePoint,
  createAuthFlowScene,
  displacedAuthFlowPoint,
} from './auth-flow-background';

describe('AuthFlowBackground', () => {
  it('builds a dense deterministic triangular surface and particle stream', () => {
    const scene = createAuthFlowScene(1187, 898);
    const duplicate = createAuthFlowScene(1187, 898);

    expect(scene).toEqual(duplicate);
    expect(scene.nodes).toHaveLength(AUTH_FLOW_MESH.desktopNodes);
    expect(scene.edges.length).toBeGreaterThan(scene.nodes.length);
    expect(scene.faces.length).toBeGreaterThan(scene.nodes.length / 2);
    expect(scene.particles).toHaveLength(AUTH_FLOW_PARTICLE_COUNT.desktop);
    expect(new Set(scene.nodes.map((node) => node.u.toFixed(3))).size).toBeGreaterThan(scene.nodes.length * 0.8);
    expect(scene.nodes.some((node) => node.anchor)).toBe(true);
    expect(scene.nodes.some((node) => node.hollow)).toBe(true);
  });

  it('keeps most particle travel time in the denser left-hand river', () => {
    const samples = Array.from({ length: 100 }, (_, index) => authFlowProgress(index / 100));
    const leftSamples = samples.filter((progress) => progress < 0.44);

    expect(leftSamples.length).toBeGreaterThanOrEqual(64);
    expect(leftSamples.length).toBeLessThanOrEqual(66);
  });

  it('creates visible continuous movement across the folded surface', () => {
    const start = authFlowSurfacePoint(-0.8, 0.25, 1187, 898, 0);
    const later = authFlowSurfacePoint(-0.8, 0.25, 1187, 898, 1.5);
    const left = authFlowSurfacePoint(-1, 0, 1187, 898, 0);
    const right = authFlowSurfacePoint(1, 0, 1187, 898, 0);

    expect(Math.hypot(later.x - start.x, later.y - start.y)).toBeGreaterThan(8);
    expect(left.y).toBeLessThan(right.y);
  });

  it('keeps particle travel inside the deliberate animation range', () => {
    const scene = createAuthFlowScene(1187, 898);

    scene.particles.forEach((particle) => {
      expect(particle.duration).toBeGreaterThanOrEqual(AUTH_FLOW_CYCLE.min);
      expect(particle.duration).toBeLessThanOrEqual(AUTH_FLOW_CYCLE.max);
    });
  });

  it('bends nearby surface points away from the pointer', () => {
    const point = { x: 200, y: 200, depth: 0.4 };
    const displaced = displacedAuthFlowPoint(point, {
      active: true,
      x: 200 - AUTH_FLOW_POINTER_RADIUS / 2,
      y: 200,
    });
    const distant = displacedAuthFlowPoint(point, {
      active: true,
      x: 200 - AUTH_FLOW_POINTER_RADIUS - 1,
      y: 200,
    });

    expect(displaced.x).toBeGreaterThan(point.x);
    expect(displaced.x - point.x).toBeLessThanOrEqual(17);
    expect(displaced.interactionStrength).toBeCloseTo(0.5);
    expect(distant).toEqual({ ...point, interactionStrength: 0 });
  });
});
