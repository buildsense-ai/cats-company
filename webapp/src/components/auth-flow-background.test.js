import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import {
  AUTH_FLOW_CYCLE,
  AUTH_FLOW_MESH,
  AUTH_FLOW_PARTICLE_COUNT,
  AUTH_FLOW_POINTER_RADIUS,
  authFlowProgress,
  authFlowPalette,
  authFlowSurfacePoint,
  createAuthFlowScene,
  displacedAuthFlowPoint,
  default as AuthFlowBackground,
} from './auth-flow-background';

describe('AuthFlowBackground', () => {
  it('uses theme-specific particles for both liquid variants', () => {
    expect(authFlowPalette('liquid')).toEqual({ r: 86, g: 98, b: 217 });
    expect(authFlowPalette('liquid-green')).toEqual({ r: 88, g: 203, b: 181 });
    expect(authFlowPalette('dark')).toEqual({ r: 124, g: 220, b: 194 });
    expect(authFlowPalette('light')).toEqual({ r: 14, g: 137, b: 104 });
  });

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

  it('renders one static frame without scheduling animation when reduced motion is requested', async () => {
    const context = {
      arc: vi.fn(),
      beginPath: vi.fn(),
      clearRect: vi.fn(),
      closePath: vi.fn(),
      fill: vi.fn(),
      lineTo: vi.fn(),
      moveTo: vi.fn(),
      setTransform: vi.fn(),
      stroke: vi.fn(),
    };
    const originalMatchMedia = window.matchMedia;
    const originalRequestAnimationFrame = window.requestAnimationFrame;
    const matchMedia = vi.fn().mockReturnValue({ matches: true });
    const requestAnimationFrame = vi.fn().mockReturnValue(1);
    Object.defineProperty(window, 'matchMedia', { configurable: true, value: matchMedia });
    Object.defineProperty(window, 'requestAnimationFrame', { configurable: true, value: requestAnimationFrame });
    const addEventListener = vi.spyOn(window, 'addEventListener');
    const getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(context);
    const getBoundingClientRect = vi.spyOn(HTMLCanvasElement.prototype, 'getBoundingClientRect').mockReturnValue({
      width: 800,
      height: 600,
      left: 0,
      top: 0,
      right: 800,
      bottom: 600,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);

    try {
      await act(async () => root.render(React.createElement(AuthFlowBackground)));

      expect(matchMedia).toHaveBeenCalledWith('(prefers-reduced-motion: reduce)');
      expect(context.clearRect).toHaveBeenCalledTimes(1);
      expect(requestAnimationFrame).not.toHaveBeenCalled();
      expect(addEventListener.mock.calls.some(([type]) => type === 'pointermove')).toBe(false);
    } finally {
      await act(async () => root.unmount());
      container.remove();
      Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia });
      Object.defineProperty(window, 'requestAnimationFrame', { configurable: true, value: originalRequestAnimationFrame });
      addEventListener.mockRestore();
      getContext.mockRestore();
      getBoundingClientRect.mockRestore();
    }
  });
});
