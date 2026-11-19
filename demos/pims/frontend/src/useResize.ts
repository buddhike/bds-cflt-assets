import {KeyboardEvent, PointerEvent, useRef, useState} from 'react';

const KEY_STEP = 24;

function load(key: string, fallback: number): number {
  try {
    const n = Number(localStorage.getItem(key));
    return n > 0 && Number.isFinite(n) ? n : fallback;
  } catch {
    return fallback;
  }
}

function save(key: string, n: number) {
  try {
    localStorage.setItem(key, String(Math.round(n)));
  } catch {
    // Remembering a size is a convenience; a blocked store just forgets it.
  }
}

export interface ResizeOptions {
  /** 'x' resizes a width and 'y' a height. */
  axis: 'x' | 'y';
  /** The size before the operator has chosen one. */
  initial: number;
  min: number;
  /** The largest size, asked when a drag starts so it can follow the window. */
  max: () => number;
  /** Where the chosen size is remembered between runs. */
  storageKey: string;
  /** True when the handle is on the near edge, so dragging towards the origin grows the panel. */
  reverse?: boolean;
}

/**
 * useResize backs a drag handle that sizes a panel. Spread `handleProps` on the
 * handle: it is a focusable separator, so the arrow keys resize it too.
 */
export function useResize({axis, initial, min, max, storageKey, reverse = false}: ResizeOptions) {
  const [size, setSize] = useState(() => Math.max(min, load(storageKey, initial)));
  const [dragging, setDragging] = useState(false);
  const latest = useRef(size);
  latest.current = size;

  const clamp = (n: number) => Math.round(Math.min(Math.max(min, max()), Math.max(min, n)));
  const sign = reverse ? -1 : 1;

  function onPointerDown(e: PointerEvent<HTMLElement>) {
    if (e.button !== 0) return;
    e.preventDefault();
    const handle = e.currentTarget;
    handle.setPointerCapture(e.pointerId);
    const origin = axis === 'x' ? e.clientX : e.clientY;
    const startSize = latest.current;
    setDragging(true);

    const move = (ev: globalThis.PointerEvent) => {
      const delta = (axis === 'x' ? ev.clientX : ev.clientY) - origin;
      setSize(clamp(startSize + sign * delta));
    };
    const stop = () => {
      handle.removeEventListener('pointermove', move);
      handle.removeEventListener('pointerup', stop);
      handle.removeEventListener('pointercancel', stop);
      setDragging(false);
      save(storageKey, latest.current);
    };
    handle.addEventListener('pointermove', move);
    handle.addEventListener('pointerup', stop);
    handle.addEventListener('pointercancel', stop);
  }

  function onKeyDown(e: KeyboardEvent<HTMLElement>) {
    const grow = axis === 'x' ? 'ArrowRight' : 'ArrowDown';
    const shrink = axis === 'x' ? 'ArrowLeft' : 'ArrowUp';
    if (e.key !== grow && e.key !== shrink) return;
    e.preventDefault();
    const next = clamp(latest.current + sign * (e.key === grow ? KEY_STEP : -KEY_STEP));
    setSize(next);
    save(storageKey, next);
  }

  const handleProps = {
    role: 'separator' as const,
    tabIndex: 0,
    'aria-orientation': (axis === 'x' ? 'vertical' : 'horizontal') as 'vertical' | 'horizontal',
    'aria-valuenow': size,
    'aria-valuemin': min,
    onPointerDown,
    onKeyDown,
  };

  return {size, dragging, handleProps};
}
