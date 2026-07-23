import React, { useEffect, useRef } from 'react';

export const AUTH_FLOW_CYCLE = Object.freeze({ min: 9000, max: 15000 });
export const AUTH_FLOW_POINTER_RADIUS = 120;
export const AUTH_FLOW_MESH = Object.freeze({ compactNodes: 64, desktopNodes: 96, neighbours: 3 });
export const AUTH_FLOW_PARTICLE_COUNT = Object.freeze({ compact: 280, desktop: 520 });

const TAU = Math.PI * 2;

function seededRandom(seed) {
  let state = seed >>> 0;
  return () => {
    state += 0x6d2b79f5;
    let value = state;
    value = Math.imul(value ^ (value >>> 15), value | 1);
    value ^= value + Math.imul(value ^ (value >>> 7), value | 61);
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
  };
}

function clamp(value, minimum, maximum) {
  return Math.max(minimum, Math.min(maximum, value));
}

function parameterDistance(first, second) {
  return Math.hypot(first.u - second.u, (first.v - second.v) * 0.72);
}

export function authFlowProgress(rawProgress) {
  const progress = ((rawProgress % 1) + 1) % 1;
  return progress < 0.65
    ? (progress / 0.65) * 0.44
    : 0.44 + ((progress - 0.65) / 0.35) * 0.56;
}

export function authFlowSurfacePoint(u, v, width, height, seconds = 0) {
  const fold = Math.sin(u * 3.4 - seconds * 0.72) * (0.62 + Math.cos(v * Math.PI) * 0.2)
    + Math.cos(u * 1.55 + v * 2.8 + seconds * 0.43) * 0.42;
  const twist = Math.sin(u * 2.1 - seconds * 0.36) * v;
  const depth = fold * 0.62 + Math.cos(u * 1.8 - v * 2.35 + seconds * 0.31) * 0.38;

  return {
    x: width * 0.5 + u * width * 0.51 + twist * width * 0.045,
    y: height * 0.52 + u * height * 0.18 + v * height * 0.16 + fold * height * 0.072,
    depth,
  };
}

export function createAuthFlowScene(width, height) {
  const safeWidth = Math.max(1, Number(width) || 1);
  const safeHeight = Math.max(1, Number(height) || 1);
  const compact = safeWidth < 700;
  const random = seededRandom(0xca75c0);
  const nodes = [];
  const edges = [];
  const faces = [];
  const particles = [];
  const stars = [];

  const nodeTarget = compact ? AUTH_FLOW_MESH.compactNodes : AUTH_FLOW_MESH.desktopNodes;
  let attempts = 0;
  while (nodes.length < nodeTarget && attempts < nodeTarget * 120) {
    attempts += 1;
    const candidate = {
      u: -1.12 + random() * 2.24,
      v: -1.08 + random() * 2.16,
    };
    if (nodes.some((node) => parameterDistance(node, candidate) < 0.115)) continue;
    const index = nodes.length;
    nodes.push({
      ...candidate,
      phase: random() * TAU,
      radius: 0.72 + random() * 0.98,
      opacity: 0.2 + random() * 0.25,
      anchor: index % 11 === 0,
      hollow: index % 19 === 0,
    });
  }

  const edgeKeys = new Set();
  const addEdge = (from, to, long = false) => {
    if (from === to) return;
    const key = from < to ? `${from}:${to}` : `${to}:${from}`;
    if (edgeKeys.has(key)) return;
    edgeKeys.add(key);
    edges.push({
      from,
      to,
      opacity: long ? 0.028 + random() * 0.035 : 0.05 + random() * 0.08,
      width: long ? 0.38 + random() * 0.16 : 0.42 + random() * 0.28,
    });
  };

  nodes.forEach((node, index) => {
    const nearest = nodes
      .map((candidate, candidateIndex) => ({
        index: candidateIndex,
        distance: candidateIndex === index ? Number.POSITIVE_INFINITY : parameterDistance(node, candidate),
      }))
      .sort((first, second) => first.distance - second.distance);
    nearest.slice(0, AUTH_FLOW_MESH.neighbours).forEach((candidate) => {
      addEdge(index, candidate.index);
    });
    if (nearest[0] && nearest[1]) {
      faces.push({
        points: [index, nearest[0].index, nearest[1].index],
        opacity: 0.007 + random() * 0.016,
      });
    }
    if (index % 13 === 0) {
      const span = nearest.find((candidate) => candidate.distance > 0.32 && candidate.distance < 0.58);
      if (span) addEdge(index, span.index, true);
    }
  });

  const particleCount = compact
    ? AUTH_FLOW_PARTICLE_COUNT.compact
    : AUTH_FLOW_PARTICLE_COUNT.desktop;
  for (let index = 0; index < particleCount; index += 1) {
    const centered = (random() + random() + random() + random()) / 4 - 0.5;
    const spray = index % 10 === 0 ? 1.9 : 1;
    particles.push({
      progress: random(),
      v: clamp(centered * 1.35 * spray, -1.02, 1.02),
      phase: random() * TAU,
      radius: 0.45 + random() * 1.05,
      opacity: 0.16 + random() * 0.36,
      duration: AUTH_FLOW_CYCLE.min
        + random() * (AUTH_FLOW_CYCLE.max - AUTH_FLOW_CYCLE.min),
      bright: index % 17 === 0,
      trail: index % 7 === 0,
    });
  }

  const starCount = compact ? 24 : 42;
  for (let index = 0; index < starCount; index += 1) {
    stars.push({
      x: random() * safeWidth,
      y: random() * safeHeight,
      radius: 0.4 + random() * 0.75,
      opacity: 0.06 + random() * 0.14,
      phase: random() * TAU,
    });
  }

  return { width: safeWidth, height: safeHeight, nodes, edges, faces, particles, stars };
}

function pointerDisplacement(point, pointer, maximum = 17) {
  if (!pointer?.active) return { x: 0, y: 0, strength: 0 };
  const deltaX = point.x - pointer.x;
  const deltaY = point.y - pointer.y;
  const distance = Math.hypot(deltaX, deltaY);
  if (distance === 0 || distance >= AUTH_FLOW_POINTER_RADIUS) {
    return { x: 0, y: 0, strength: 0 };
  }
  const strength = 1 - distance / AUTH_FLOW_POINTER_RADIUS;
  const displacement = strength * strength * maximum;
  return {
    x: (deltaX / distance) * displacement,
    y: (deltaY / distance) * displacement,
    strength,
  };
}

export function displacedAuthFlowPoint(point, pointer, maximum) {
  const displacement = pointerDisplacement(point, pointer, maximum);
  return {
    ...point,
    x: point.x + displacement.x,
    y: point.y + displacement.y,
    interactionStrength: displacement.strength,
  };
}

function meshNodePosition(node, seconds, scene, pointer) {
  const breathingV = node.v + Math.sin(seconds * 0.45 + node.phase) * 0.025;
  const point = authFlowSurfacePoint(node.u, breathingV, scene.width, scene.height, seconds);
  return displacedAuthFlowPoint(point, pointer);
}

function particlePosition(particle, milliseconds, scene, pointer, progressOffset = 0) {
  const rawProgress = (particle.progress + milliseconds / particle.duration + progressOffset + 2) % 1;
  const progress = authFlowProgress(rawProgress);
  const u = -1.16 + progress * 2.32;
  const seconds = milliseconds / 1000;
  const v = particle.v + Math.sin(seconds * 0.75 + particle.phase + u * 2.2) * 0.055;
  const point = authFlowSurfacePoint(u, v, scene.width, scene.height, seconds);
  const edgeFade = Math.min(1, progress / 0.055, (1 - progress) / 0.055);
  return { ...displacedAuthFlowPoint(point, pointer, 10), edgeFade };
}

function rgba(palette, opacity) {
  return `rgba(${palette.r}, ${palette.g}, ${palette.b}, ${opacity})`;
}

export default function AuthFlowBackground() {
  const canvasRef = useRef(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    let context;
    try {
      context = canvas?.getContext?.('2d');
    } catch {
      return undefined;
    }
    if (!canvas || !context) return undefined;

    const pointer = { active: false, x: 0, y: 0 };
    const reduceMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
    let animationFrame = 0;
    let lastFrame = 0;
    let scene = createAuthFlowScene(1, 1);

    const palette = () => document.documentElement.dataset.theme === 'dark'
      ? { r: 124, g: 220, b: 194 }
      : { r: 14, g: 137, b: 104 };

    const draw = (timestamp = 0) => {
      const seconds = timestamp / 1000;
      const color = palette();
      context.clearRect(0, 0, scene.width, scene.height);

      scene.stars.forEach((star) => {
        const driftX = Math.sin(seconds * 0.16 + star.phase) * 3;
        const driftY = Math.cos(seconds * 0.13 + star.phase) * 2;
        context.beginPath();
        context.arc(star.x + driftX, star.y + driftY, star.radius, 0, TAU);
        context.fillStyle = rgba(color, star.opacity);
        context.fill();
      });

      const positions = scene.nodes.map((node) => meshNodePosition(node, seconds, scene, pointer));
      scene.faces.forEach((face) => {
        const points = face.points.map((index) => positions[index]);
        const depth = clamp((points.reduce((sum, point) => sum + point.depth, 0) / 3 + 1.4) / 2.8, 0.2, 1);
        context.beginPath();
        context.moveTo(points[0].x, points[0].y);
        context.lineTo(points[1].x, points[1].y);
        context.lineTo(points[2].x, points[2].y);
        context.closePath();
        context.fillStyle = rgba(color, face.opacity * (0.45 + depth * 0.75));
        context.fill();
      });
      scene.edges.forEach((edge) => {
        const start = positions[edge.from];
        const end = positions[edge.to];
        const interaction = Math.max(start.interactionStrength, end.interactionStrength);
        const depth = clamp((start.depth + end.depth + 2) / 4, 0.25, 1);
        context.beginPath();
        context.moveTo(start.x, start.y);
        context.lineTo(end.x, end.y);
        context.lineWidth = edge.width * (0.8 + depth * 0.35);
        context.strokeStyle = rgba(color, edge.opacity * (0.7 + depth * 0.65) * (1 - interaction * 0.48));
        context.stroke();
      });

      positions.forEach((position, index) => {
        const node = scene.nodes[index];
        const depth = clamp((position.depth + 1.4) / 2.8, 0.25, 1);
        const radius = node.radius * (0.72 + depth * 0.62) * (node.anchor ? 2.1 : 1);
        const opacity = Math.min(0.82, node.opacity * (0.62 + depth * 0.72) + (node.anchor ? 0.18 : 0));
        context.beginPath();
        context.arc(position.x, position.y, radius, 0, TAU);
        if (node.anchor) {
          context.shadowColor = rgba(color, 0.3);
          context.shadowBlur = 10;
        }
        if (node.hollow) {
          context.lineWidth = 0.85;
          context.strokeStyle = rgba(color, opacity);
          context.stroke();
        } else {
          context.fillStyle = rgba(color, opacity);
          context.fill();
        }
        context.shadowBlur = 0;
      });

      scene.particles.forEach((particle) => {
        const position = particlePosition(particle, timestamp, scene, pointer);
        const depth = clamp((position.depth + 1.4) / 2.8, 0.22, 1);
        const opacity = particle.opacity * position.edgeFade * (0.62 + depth * 0.7);

        if (particle.trail) {
          const tail = particlePosition(particle, timestamp, scene, pointer, -0.007);
          context.beginPath();
          context.moveTo(tail.x, tail.y);
          context.lineTo(position.x, position.y);
          context.lineWidth = 0.55 + depth * 0.35;
          context.strokeStyle = rgba(color, opacity * 0.34);
          context.stroke();
        }

        context.beginPath();
        context.arc(
          position.x,
          position.y,
          particle.radius * (0.7 + depth * 0.65) * (particle.bright ? 1.65 : 1),
          0,
          TAU,
        );
        if (particle.bright) {
          context.shadowColor = rgba(color, 0.38);
          context.shadowBlur = 9;
        }
        context.fillStyle = rgba(color, Math.min(0.78, opacity + (particle.bright ? 0.16 : 0)));
        context.fill();
        context.shadowBlur = 0;
      });
    };

    const resize = () => {
      const bounds = canvas.getBoundingClientRect();
      const width = Math.max(1, bounds.width);
      const height = Math.max(1, bounds.height);
      const pixelRatio = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.round(width * pixelRatio);
      canvas.height = Math.round(height * pixelRatio);
      context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0);
      scene = createAuthFlowScene(width, height);
      draw(lastFrame);
    };

    const updatePointer = (event) => {
      const bounds = canvas.getBoundingClientRect();
      pointer.x = event.clientX - bounds.left;
      pointer.y = event.clientY - bounds.top;
      pointer.active = pointer.x >= 0 && pointer.y >= 0
        && pointer.x <= scene.width && pointer.y <= scene.height;
    };
    const clearPointer = () => {
      pointer.active = false;
    };
    const animate = (timestamp) => {
      if (timestamp - lastFrame >= 33) {
        lastFrame = timestamp;
        draw(timestamp);
      }
      animationFrame = window.requestAnimationFrame(animate);
    };

    const resizeObserver = typeof ResizeObserver === 'function'
      ? new ResizeObserver(resize)
      : null;
    const themeObserver = typeof MutationObserver === 'function'
      ? new MutationObserver(() => draw(lastFrame))
      : null;
    resizeObserver?.observe(canvas);
    themeObserver?.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    window.addEventListener('resize', resize);
    resize();
    if (!reduceMotion) {
      window.addEventListener('pointermove', updatePointer, { passive: true });
      window.addEventListener('blur', clearPointer);
      document.addEventListener('mouseleave', clearPointer);
      animationFrame = window.requestAnimationFrame(animate);
    }

    return () => {
      resizeObserver?.disconnect();
      themeObserver?.disconnect();
      window.removeEventListener('resize', resize);
      if (!reduceMotion) {
        window.removeEventListener('pointermove', updatePointer);
        window.removeEventListener('blur', clearPointer);
        document.removeEventListener('mouseleave', clearPointer);
      }
      if (animationFrame) window.cancelAnimationFrame(animationFrame);
    };
  }, []);

  return <canvas ref={canvasRef} className="oc-auth-flow-background" aria-hidden="true" />;
}
