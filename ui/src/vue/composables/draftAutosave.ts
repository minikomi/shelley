// Vue port of hooks/useDraftAutosave.ts. The state lives in a closure; the Vue
// wrapper only adds an onUnmounted trailing save.
import { onUnmounted } from "vue";

export interface DraftAutosaveOptions {
  baseDelayMs?: number;
  maxDelayMs?: number;
}

export interface DraftAutosaveControls {
  schedule(value: string): void;
  cancel(): void;
  flush(): void;
  reset(): void;
}

export function createDraftAutosave(
  save: (value: string) => Promise<void>,
  options: DraftAutosaveOptions = {},
): DraftAutosaveControls {
  const { baseDelayMs = 600, maxDelayMs = 10_000 } = options;
  const saveFn = save;
  // Allow the caller to swap the save fn over time without re-creating controls.
  // (Vue callers typically pass a stable closure, but keep parity with React.)

  let timer: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let inFlightGeneration: number | null = null;
  let pendingValue: string | null = null;
  let lastSaved: string | null = null;
  let failureCount = 0;

  const computeDelay = () => {
    if (failureCount === 0) return baseDelayMs;
    return Math.min(baseDelayMs * Math.pow(2, failureCount), maxDelayMs);
  };

  const run = async () => {
    const runGeneration = generation;
    if (inFlightGeneration === runGeneration) return;
    const value = pendingValue;
    if (value === null) return;
    if (value === lastSaved) {
      pendingValue = null;
      return;
    }
    inFlightGeneration = runGeneration;
    try {
      await saveFn(value);
      if (generation !== runGeneration) return;
      lastSaved = value;
      failureCount = 0;
      if (pendingValue === value) pendingValue = null;
    } catch (err) {
      if (generation !== runGeneration) return;
      failureCount += 1;
      console.warn("Draft autosave failed; will retry", err);
    } finally {
      if (generation === runGeneration) {
        if (inFlightGeneration === runGeneration) inFlightGeneration = null;
        if (pendingValue !== null) {
          if (timer) clearTimeout(timer);
          timer = setTimeout(run, computeDelay());
        }
      }
    }
  };

  const schedule = (value: string) => {
    pendingValue = value;
    if (timer) clearTimeout(timer);
    timer = setTimeout(run, computeDelay());
  };

  const cancel = () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    pendingValue = null;
  };

  const flush = () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    void run();
  };

  const reset = () => {
    generation++;
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    pendingValue = null;
    lastSaved = null;
    failureCount = 0;
  };

  return { schedule, cancel, flush, reset };
}

export function useDraftAutosave(
  save: (value: string) => Promise<void>,
  options: DraftAutosaveOptions = {},
): DraftAutosaveControls {
  const controls = createDraftAutosave(save, options);
  onUnmounted(controls.flush);
  return controls;
}
