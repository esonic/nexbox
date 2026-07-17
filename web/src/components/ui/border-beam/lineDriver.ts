/**
 * Shared, frame-capped driver for the paint-heavy `line` beam.
 *
 * Animating registered custom properties with CSS makes Chromium sample and
 * repaint the gradient masks at the display refresh rate (60-120 Hz), even
 * though the glow moves slowly. Update all motion variables together at 20 fps
 * instead: this keeps the motion fluid while bounding paint work independently
 * of the display's refresh rate. 20 fps divides evenly into common 60/120 Hz
 * refresh rates, avoiding uneven frame cadence while the slow glow travels.
 */

interface LineInstance {
  el: HTMLElement;
  id: string;
  duration: number;
  startedAt: number;
  elapsedBefore: number;
}

type ValueKeyframe = readonly [offset: number, value: number];

const instances = new Set<LineInstance>();
const elapsedByElement = new WeakMap<HTMLElement, number>();

const TARGET_FPS = 20;
const FRAME_INTERVAL = 1000 / TARGET_FPS;

const WIDTH_FRAMES: readonly ValueKeyframe[] = [
  [0, 0.6],
  [0.1, 0.85],
  [0.2, 1.1],
  [0.3, 1.3],
  [0.4, 1.45],
  [0.5, 1.5],
  [0.6, 1.45],
  [0.7, 1.3],
  [0.8, 1.1],
  [0.9, 0.85],
  [1, 0.6],
];

const HEIGHT_FRAMES: readonly ValueKeyframe[] = [
  [0, 0.8],
  [0.25, 1.25],
  [0.55, 0.85],
  [0.8, 1.3],
  [1, 0.8],
];

const SPIKE_FRAMES: readonly ValueKeyframe[] = [
  [0, 0.8],
  [0.25, 1.3],
  [0.5, 0.9],
  [0.75, 1.4],
  [1, 0.8],
];

const SPIKE2_FRAMES: readonly ValueKeyframe[] = [
  [0, 1.2],
  [0.25, 0.7],
  [0.5, 1.4],
  [0.75, 0.8],
  [1, 1.2],
];

let rafId: number | null = null;
let lastFrame = 0;

function now(): number {
  return typeof performance !== 'undefined' ? performance.now() : Date.now();
}

function phaseAt(seconds: number, period: number): number {
  return (seconds % period) / period;
}

function interpolate(
  frames: readonly ValueKeyframe[],
  phase: number,
  easeSegments = false
): number {
  for (let i = 1; i < frames.length; i += 1) {
    const [endOffset, endValue] = frames[i];
    if (phase > endOffset) continue;

    const [startOffset, startValue] = frames[i - 1];
    let progress = (phase - startOffset) / (endOffset - startOffset);
    if (easeSegments) {
      progress = (1 - Math.cos(Math.PI * progress)) / 2;
    }
    return startValue + (endValue - startValue) * progress;
  }
  return frames[frames.length - 1][1];
}

function update(instance: LineInstance, timestamp: number): void {
  const { el, id, duration, startedAt, elapsedBefore } = instance;
  const seconds = (elapsedBefore + timestamp - startedAt) / 1000;
  const travelPhase = phaseAt(seconds, duration);
  const edge = Math.max(0, Math.min(1, travelPhase / 0.06, (1 - travelPhase) / 0.06));

  el.style.setProperty(`--beam-x-${id}`, travelPhase.toFixed(4));
  el.style.setProperty(`--beam-w-${id}`, interpolate(WIDTH_FRAMES, travelPhase).toFixed(4));
  el.style.setProperty(`--beam-edge-${id}`, edge.toFixed(4));
  el.style.setProperty(
    `--beam-h-${id}`,
    interpolate(HEIGHT_FRAMES, phaseAt(seconds, duration * 1.3), true).toFixed(4)
  );
  el.style.setProperty(
    `--beam-spike-${id}`,
    interpolate(SPIKE_FRAMES, phaseAt(seconds, duration * 1.33), true).toFixed(4)
  );
  el.style.setProperty(
    `--beam-spike2-${id}`,
    interpolate(SPIKE2_FRAMES, phaseAt(seconds, duration * 1.7), true).toFixed(4)
  );
}

function frame(timestamp: number): void {
  rafId = requestAnimationFrame(frame);

  const elapsed = timestamp - lastFrame;
  if (elapsed < FRAME_INTERVAL) return;

  // Preserve the remainder so displays whose refresh rate is not an exact
  // multiple still average the requested update frequency over time.
  lastFrame = timestamp - (elapsed % FRAME_INTERVAL);
  instances.forEach((instance) => update(instance, timestamp));
}

function startLoop(): void {
  if (rafId != null) return;
  lastFrame = 0;
  rafId = requestAnimationFrame(frame);
}

function stopLoopIfIdle(): void {
  if (instances.size > 0 || rafId == null) return;
  cancelAnimationFrame(rafId);
  rafId = null;
}

export function registerLineInstance(
  el: HTMLElement,
  id: string,
  duration: number
): () => void {
  const instance: LineInstance = {
    el,
    id,
    duration,
    startedAt: now(),
    elapsedBefore: elapsedByElement.get(el) ?? 0,
  };

  instances.add(instance);
  update(instance, instance.startedAt);
  startLoop();

  return () => {
    elapsedByElement.set(el, instance.elapsedBefore + now() - instance.startedAt);
    instances.delete(instance);
    stopLoopIfIdle();
  };
}
