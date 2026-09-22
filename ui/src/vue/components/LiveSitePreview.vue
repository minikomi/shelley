<template>
  <Teleport to="body">
    <section
      v-if="livePreviewOpen"
      class="live-site-preview"
      data-testid="live-site-preview"
      :style="windowStyle"
      aria-label="Live site preview"
    >
      <header class="live-site-preview-header" @pointerdown="startDrag">
        <i class="pi pi-arrows-alt live-site-preview-grip" aria-hidden="true" />
        <span class="live-site-preview-dot" :class="{ loading }" />
        <strong v-if="!editingEndpoint">Preview</strong>
        <form
          v-if="editingEndpoint"
          class="live-site-preview-endpoint-form"
          @submit.prevent="commitEndpoint"
          @pointerdown.stop
        >
          <span>:</span>
          <input
            ref="endpointInput"
            v-model="portText"
            class="live-site-preview-port-input"
            data-testid="live-site-preview-port-input"
            inputmode="numeric"
            aria-label="Preview port"
            :aria-invalid="Boolean(endpointError)"
            :title="endpointError || 'Preview port'"
            @input="endpointError = ''"
            @keydown.esc="cancelEndpointEdit"
          />
          <input
            v-model="pathText"
            class="live-site-preview-path-input"
            data-testid="live-site-preview-path-input"
            aria-label="Preview path"
            :aria-invalid="Boolean(endpointError)"
            :title="endpointError || 'Preview path'"
            placeholder="/"
            @input="endpointError = ''"
            @keydown.esc="cancelEndpointEdit"
          />
          <button
            type="submit"
            class="live-site-preview-endpoint-save"
            data-testid="live-site-preview-endpoint-save"
            aria-label="Save preview endpoint"
            title="Save endpoint"
          >
            <i class="pi pi-check" aria-hidden="true" />
          </button>
        </form>
        <button
          v-else
          type="button"
          class="live-site-preview-endpoint"
          data-testid="live-site-preview-endpoint"
          aria-label="Change preview endpoint"
          :title="`Change preview endpoint (${endpointLabel}). The previewed site runs with your Shelley session; only preview code you trust.`"
          @pointerdown.stop
          @click="beginEndpointEdit"
        >
          {{ endpointLabel }}
        </button>
        <div v-if="!editingEndpoint" class="live-site-preview-actions">
          <button
            type="button"
            data-testid="live-site-preview-viewport-toggle"
            :aria-label="`Switch to ${viewportMode === 'desktop' ? 'mobile' : 'desktop'} viewport`"
            :title="viewportMode === 'desktop' ? 'Mobile viewport' : 'Desktop viewport'"
            @pointerdown.stop
            @click="toggleViewportMode"
          >
            <i
              class="pi"
              :class="viewportMode === 'desktop' ? 'pi-mobile' : 'pi-desktop'"
              aria-hidden="true"
            />
          </button>
          <a
            :href="siteUrl"
            target="_blank"
            rel="noopener noreferrer"
            aria-label="Open preview in a new tab"
            title="Open in new tab"
            @pointerdown.stop
          >
            <i class="pi pi-external-link" aria-hidden="true" />
          </a>
          <button
            type="button"
            aria-label="Refresh preview"
            title="Refresh"
            @pointerdown.stop
            @click="refreshLivePreview"
          >
            <i class="pi pi-refresh" aria-hidden="true" />
          </button>
          <button
            type="button"
            data-testid="live-site-preview-close"
            aria-label="Close preview"
            title="Close"
            @pointerdown.stop
            @click="closeLivePreview"
          >
            <i class="pi pi-times" aria-hidden="true" />
          </button>
        </div>
      </header>

      <div class="live-site-preview-body">
        <iframe
          :key="frameKey"
          class="live-site-preview-frame"
          data-testid="live-site-preview-frame"
          :src="frameSrc"
          :style="frameStyle"
          title="Live preview"
          sandbox="allow-scripts allow-same-origin allow-forms"
          allow="
            camera 'none';
            microphone 'none';
            geolocation 'none';
            clipboard-read 'none';
            clipboard-write 'none';
          "
          @load="frameLoaded"
        />
        <div v-if="loading" class="live-site-preview-loading" aria-live="polite">
          Loading preview…
        </div>
      </div>

      <div class="live-site-preview-resize left" @pointerdown="startResize($event, 'left')" />
      <div class="live-site-preview-resize right" @pointerdown="startResize($event, 'right')" />
    </section>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  closeLivePreview,
  livePreviewOpen,
  livePreviewPath,
  livePreviewPort,
  livePreviewRevision,
  openLivePreview,
  previewURL,
  refreshLivePreview,
  syncLivePreviewPath,
} from "../composables/livePreview";

const DESKTOP_WIDTH = 1280;
const MOBILE_WIDTH = 390;
const DESKTOP_SIZE = { width: 480, height: 340 };
const MOBILE_SIZE = { width: 260, height: 400 };
const HEADER_HEIGHT = window.matchMedia("(pointer: coarse)").matches ? 44 : 34;
const MIN_WIDTH = 240;
const MIN_HEIGHT = 240;
const GAP = 16;

type ViewportMode = "desktop" | "mobile";
type ResizeCorner = "left" | "right";

const width = ref(DESKTOP_SIZE.width);
const height = ref(DESKTOP_SIZE.height);
const left = ref(0);
const top = ref(0);
const loading = ref(true);
const portText = ref(String(livePreviewPort.value));
const pathText = ref(livePreviewPath.value);
const editingEndpoint = ref(false);
const endpointError = ref("");
const endpointInput = ref<HTMLInputElement | null>(null);
const viewportMode = ref<ViewportMode>("desktop");
const panelSizes: Record<ViewportMode, { width: number; height: number }> = {
  desktop: DESKTOP_SIZE,
  mobile: MOBILE_SIZE,
};

const hostname = window.__SHELLEY_INIT__?.hostname ?? window.location.hostname;
const url = computed(() => previewURL(livePreviewPort.value, livePreviewPath.value));
const siteUrl = computed(
  () => `https://${hostname}:${livePreviewPort.value}${livePreviewPath.value}`,
);
const endpointLabel = computed(() => `:${livePreviewPort.value}${livePreviewPath.value}`);
const frameKey = computed(() => `${livePreviewPort.value}-${livePreviewRevision.value}`);
const frameSrc = ref(url.value);
const windowStyle = computed(() => ({
  "--header-height": `${HEADER_HEIGHT}px`,
  width: `${width.value}px`,
  height: `${height.value}px`,
  left: `${left.value}px`,
  top: `${top.value}px`,
}));
const frameStyle = computed(() => {
  const bodyHeight = Math.max(1, height.value - HEADER_HEIGHT);
  const viewportWidth = viewportMode.value === "desktop" ? DESKTOP_WIDTH : MOBILE_WIDTH;
  const scale = width.value / viewportWidth;
  return {
    width: `${viewportWidth}px`,
    height: `${bodyHeight / scale}px`,
    transform: `scale(${scale})`,
  };
});

type PointerAction =
  | { kind: "drag"; pointerId: number; x: number; y: number; left: number; top: number }
  | {
      kind: "resize";
      corner: ResizeCorner;
      pointerId: number;
      x: number;
      y: number;
      width: number;
      height: number;
      left: number;
    };

let pointerAction: PointerAction | null = null;

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function clampWindow() {
  width.value = Math.min(width.value, window.innerWidth);
  height.value = Math.min(height.value, window.innerHeight);
  left.value = clamp(left.value, 0, Math.max(0, window.innerWidth - width.value));
  top.value = clamp(top.value, 0, Math.max(0, window.innerHeight - height.value));
}

function placeBottomRight() {
  width.value = Math.min(width.value, window.innerWidth);
  height.value = Math.min(height.value, window.innerHeight);
  left.value = Math.max(0, window.innerWidth - width.value - GAP);
  top.value = Math.max(0, window.innerHeight - height.value - GAP);
}

function startDrag(event: PointerEvent) {
  if (
    event.button !== 0 ||
    (event.target instanceof Element && event.target.closest("input, button, a"))
  ) {
    return;
  }
  event.preventDefault();
  (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  pointerAction = {
    kind: "drag",
    pointerId: event.pointerId,
    x: event.clientX,
    y: event.clientY,
    left: left.value,
    top: top.value,
  };
  window.addEventListener("pointermove", movePointer);
  window.addEventListener("pointerup", stopPointer);
  window.addEventListener("pointercancel", stopPointer);
}

function startResize(event: PointerEvent, corner: ResizeCorner) {
  if (event.button !== 0) return;
  event.preventDefault();
  (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  pointerAction = {
    kind: "resize",
    corner,
    pointerId: event.pointerId,
    x: event.clientX,
    y: event.clientY,
    width: width.value,
    height: height.value,
    left: left.value,
  };
  window.addEventListener("pointermove", movePointer);
  window.addEventListener("pointerup", stopPointer);
  window.addEventListener("pointercancel", stopPointer);
}

function movePointer(event: PointerEvent) {
  const action = pointerAction;
  if (!action || action.pointerId !== event.pointerId) return;
  if (action.kind === "drag") {
    left.value = clamp(
      action.left + event.clientX - action.x,
      0,
      Math.max(0, window.innerWidth - width.value),
    );
    top.value = clamp(
      action.top + event.clientY - action.y,
      0,
      Math.max(0, window.innerHeight - height.value),
    );
    return;
  }

  const dx = event.clientX - action.x;
  if (action.corner === "left") {
    const right = action.left + action.width;
    width.value = clamp(action.width - dx, Math.min(MIN_WIDTH, right), right);
    left.value = right - width.value;
  } else {
    const maxWidth = window.innerWidth - action.left;
    width.value = clamp(action.width + dx, Math.min(MIN_WIDTH, maxWidth), maxWidth);
  }
  const maxHeight = window.innerHeight - top.value;
  height.value = clamp(
    action.height + event.clientY - action.y,
    Math.min(MIN_HEIGHT, maxHeight),
    maxHeight,
  );
}

function stopPointer(event: PointerEvent) {
  if (!pointerAction || pointerAction.pointerId !== event.pointerId) return;
  pointerAction = null;
  window.removeEventListener("pointermove", movePointer);
  window.removeEventListener("pointerup", stopPointer);
  window.removeEventListener("pointercancel", stopPointer);
}

function beginEndpointEdit() {
  portText.value = String(livePreviewPort.value);
  pathText.value = livePreviewPath.value;
  endpointError.value = "";
  editingEndpoint.value = true;
  void nextTick(() => endpointInput.value?.select());
}

function cancelEndpointEdit() {
  editingEndpoint.value = false;
  endpointError.value = "";
}

function commitEndpoint() {
  try {
    openLivePreview(Number(portText.value), pathText.value || "/");
    cancelEndpointEdit();
  } catch (error) {
    endpointError.value = error instanceof Error ? error.message : "Invalid preview endpoint";
  }
}

function frameLoaded(event: Event) {
  loading.value = false;
  const frame = event.target as HTMLIFrameElement;
  const href = frame.contentDocument?.location.href;
  if (href) syncLivePreviewPath(href);
}

function toggleViewportMode() {
  panelSizes[viewportMode.value] = { width: width.value, height: height.value };
  viewportMode.value = viewportMode.value === "desktop" ? "mobile" : "desktop";
  const size = panelSizes[viewportMode.value];
  const maxWidth = window.innerWidth - left.value;
  const maxHeight = window.innerHeight - top.value;
  width.value = clamp(size.width, Math.min(MIN_WIDTH, maxWidth), maxWidth);
  height.value = clamp(size.height, Math.min(MIN_HEIGHT, maxHeight), maxHeight);
}

watch([livePreviewOpen, frameKey], () => {
  frameSrc.value = url.value;
  loading.value = true;
});

watch([livePreviewPort, livePreviewPath], ([port, path]) => {
  portText.value = String(port);
  pathText.value = path;
});

watch(livePreviewOpen, (open) => {
  if (open) placeBottomRight();
});

onMounted(() => {
  window.addEventListener("resize", clampWindow);
});

onBeforeUnmount(() => {
  window.removeEventListener("resize", clampWindow);
  window.removeEventListener("pointermove", movePointer);
  window.removeEventListener("pointerup", stopPointer);
  window.removeEventListener("pointercancel", stopPointer);
});
</script>

<style scoped>
.live-site-preview {
  position: fixed;
  z-index: 1000;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border-radius: 7px;
  background: var(--bg-base);
  box-shadow:
    0 0 0 1px var(--border),
    0 14px 38px rgb(0 0 0 / 27%);
}

.live-site-preview-header {
  box-sizing: border-box;
  display: flex;
  height: var(--header-height);
  flex: 0 0 var(--header-height);
  align-items: center;
  justify-content: space-between;
  gap: 7px;
  padding: 0 7px 0 11px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-tertiary);
  cursor: grab;
  touch-action: none;
  user-select: none;
}

.live-site-preview-header:active {
  cursor: grabbing;
}

.live-site-preview-header strong {
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 650;
}

.live-site-preview-grip {
  flex: 0 0 auto;
  color: var(--text-tertiary);
  font-size: 11px;
}

.live-site-preview-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: #22c55e;
  box-shadow: 0 0 0 2px rgb(34 197 94 / 13%);
}

.live-site-preview-dot.loading {
  background: #f59e0b;
  box-shadow: 0 0 0 2px rgb(245 158 11 / 13%);
}

.live-site-preview-endpoint-form {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  color: var(--text-secondary);
  font-size: 12px;
}

.live-site-preview-endpoint-form input {
  width: 42px;
  height: 22px;
  padding: 0 4px;
  border: 1px solid var(--border);
  border-radius: 3px;
  background: var(--bg-base);
  color: var(--text-primary);
  font: inherit;
}

.live-site-preview-endpoint-form input[aria-invalid="true"] {
  border-color: var(--error-text);
}

.live-site-preview-endpoint-form .live-site-preview-path-input {
  min-width: 48px;
  flex: 1;
  margin-left: 3px;
}

.live-site-preview-endpoint-save {
  display: grid;
  width: 24px;
  height: 22px;
  flex: 0 0 auto;
  margin-left: 3px;
  place-items: center;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
}

.live-site-preview-endpoint-save:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.live-site-preview-endpoint {
  min-width: 0;
  max-width: 150px;
  height: 24px;
  padding: 0 2px;
  border: 0;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  font: 12px/1 system-ui;
  font-variant-numeric: tabular-nums;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-site-preview-actions {
  display: flex;
  margin-left: auto;
  gap: 2px;
}

.live-site-preview-actions a,
.live-site-preview-actions button {
  display: grid;
  width: 24px;
  height: 24px;
  place-items: center;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  text-decoration: none;
}

.live-site-preview-actions a:hover,
.live-site-preview-actions button:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.live-site-preview-body {
  position: relative;
  min-height: 0;
  flex: 1;
  overflow: hidden;
  background: var(--bg-secondary);
}

.live-site-preview-frame {
  position: absolute;
  top: 0;
  left: 0;
  display: block;
  border: 0;
  transform-origin: top left;
}

.live-site-preview-loading {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  background: color-mix(in srgb, var(--bg-secondary) 82%, transparent);
  color: var(--text-secondary);
  font-size: 13px;
}

.live-site-preview-resize {
  position: absolute;
  bottom: 0;
  width: 18px;
  height: 18px;
  touch-action: none;
}

.live-site-preview-resize.right {
  right: 0;
  cursor: nwse-resize;
}

.live-site-preview-resize.left {
  left: 0;
  cursor: nesw-resize;
}

.live-site-preview-resize::after {
  position: absolute;
  bottom: 4px;
  width: 7px;
  height: 7px;
  border-bottom: 1px solid var(--text-tertiary);
  content: "";
}

.live-site-preview-resize.right::after {
  right: 4px;
  border-right: 1px solid var(--text-tertiary);
}

.live-site-preview-resize.left::after {
  left: 4px;
  border-left: 1px solid var(--text-tertiary);
}

@media (pointer: coarse) {
  .live-site-preview-actions a,
  .live-site-preview-actions button {
    width: 36px;
    height: 36px;
  }

  .live-site-preview-resize {
    width: 28px;
    height: 28px;
  }
}
</style>
