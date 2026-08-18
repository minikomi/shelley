<template>
  <section
    v-show="visible"
    ref="dockRef"
    class="secondary-dock"
    :class="[
      `secondary-dock-${effectivePlacement}`,
      { 'secondary-dock-collapsed': collapsed, 'secondary-dock-fullscreen': fullscreen },
    ]"
    :style="dockStyle"
    data-testid="secondary-dock"
  >
    <div
      v-if="!collapsed && !fullscreen"
      class="secondary-dock-resize-handle"
      :aria-label="effectivePlacement === 'bottom' ? 'Resize dock height' : 'Resize dock width'"
      @mousedown="startResize"
    >
      <div class="secondary-dock-resize-grip" />
    </div>

    <header class="secondary-dock-header">
      <button
        class="secondary-dock-title"
        :aria-label="collapsed ? `Expand ${title}` : `Collapse ${title}`"
        :title="collapsed ? `Expand ${title}` : `Collapse ${title}`"
        @click="toggleCollapsed"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M4 17l6-6 4 4 6-6"
          />
          <path stroke-linecap="round" :stroke-width="2" d="M4 21h16" />
        </svg>
        <span>{{ title }}</span>
      </button>

      <div class="secondary-dock-controls">
        <button
          v-if="!isNarrow && !collapsed && !fullscreen"
          class="secondary-dock-button"
          :aria-label="
            effectivePlacement === 'bottom' ? 'Move dock to right' : 'Move dock to bottom'
          "
          :title="effectivePlacement === 'bottom' ? 'Move dock to right' : 'Move dock to bottom'"
          @click="togglePlacement"
        >
          <svg
            v-if="effectivePlacement === 'bottom'"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <rect x="4" y="4" width="16" height="16" rx="2" :stroke-width="2" />
            <path d="M14 4v16" :stroke-width="2" />
          </svg>
          <svg v-else fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <rect x="4" y="4" width="16" height="16" rx="2" :stroke-width="2" />
            <path d="M4 14h16" :stroke-width="2" />
          </svg>
        </button>
        <button
          v-if="!collapsed"
          class="secondary-dock-button"
          :aria-label="fullscreen ? 'Exit dock fullscreen' : 'Fullscreen dock'"
          :title="fullscreen ? 'Exit fullscreen' : 'Fullscreen dock'"
          @click="toggleFullscreen"
        >
          <svg v-if="fullscreen" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path :stroke-width="2" d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" />
          </svg>
          <svg v-else fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path :stroke-width="2" d="M9 4H4v5M15 4h5v5M9 20H4v-5M15 20h5v-5" />
          </svg>
        </button>
        <button
          class="secondary-dock-button"
          :aria-label="collapsed ? `Expand ${title}` : `Collapse ${title}`"
          :title="collapsed ? `Expand ${title}` : `Collapse ${title}`"
          @click="toggleCollapsed"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              :d="collapseIconPath"
            />
          </svg>
        </button>
      </div>
    </header>

    <div class="secondary-dock-content" :aria-hidden="collapsed">
      <slot />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";

export type SecondaryDockPlacement = "bottom" | "right";
export interface SecondaryDockLayout {
  placement: SecondaryDockPlacement;
  collapsed: boolean;
  fullscreen: boolean;
}

const PLACEMENT_KEY = "shelley-secondary-dock-placement";
const BOTTOM_SIZE_KEY = "shelley-secondary-dock-bottom-height";
const RIGHT_SIZE_KEY = "shelley-secondary-dock-right-width";
const NARROW_QUERY = "(max-width: 767px)";

const props = withDefaults(
  defineProps<{
    title: string;
    visible?: boolean;
  }>(),
  { visible: true },
);

const emit = defineEmits<{
  (e: "layout-change", layout: SecondaryDockLayout): void;
}>();

function storedNumber(key: string, fallback: number): number {
  const value = Number(localStorage.getItem(key));
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

const storedPlacement = localStorage.getItem(PLACEMENT_KEY);
const placement = ref<SecondaryDockPlacement>(storedPlacement === "right" ? "right" : "bottom");
const bottomHeight = ref(storedNumber(BOTTOM_SIZE_KEY, 300));
const rightWidth = ref(storedNumber(RIGHT_SIZE_KEY, 420));
const collapsed = ref(false);
const fullscreen = ref(false);
const dockRef = ref<HTMLElement | null>(null);
const narrowMq = window.matchMedia(NARROW_QUERY);
const isNarrow = ref(narrowMq.matches);
const effectivePlacement = computed<SecondaryDockPlacement>(() =>
  isNarrow.value ? "bottom" : placement.value,
);

const dockStyle = computed(() => {
  if (fullscreen.value || collapsed.value) return undefined;
  return effectivePlacement.value === "bottom"
    ? { height: `${bottomHeight.value}px` }
    : { width: `${rightWidth.value}px` };
});

const collapseIconPath = computed(() => {
  if (collapsed.value)
    return effectivePlacement.value === "bottom" ? "M6 9l6 6 6-6" : "M9 18l6-6-6-6";
  return effectivePlacement.value === "bottom" ? "M6 15l6-6 6 6" : "M15 18l-6-6 6-6";
});

function publishLayout() {
  emit("layout-change", {
    placement: effectivePlacement.value,
    collapsed: collapsed.value,
    fullscreen: fullscreen.value,
  });
}

watch([effectivePlacement, collapsed, fullscreen], publishLayout, { immediate: true });
watch(
  () => props.visible,
  (visible) => {
    if (!visible && fullscreen.value) fullscreen.value = false;
  },
);

function expand() {
  collapsed.value = false;
}

defineExpose({ expand });

function toggleCollapsed() {
  if (fullscreen.value) fullscreen.value = false;
  collapsed.value = !collapsed.value;
}

function toggleFullscreen() {
  fullscreen.value = !fullscreen.value;
  collapsed.value = false;
}

function togglePlacement() {
  placement.value = placement.value === "bottom" ? "right" : "bottom";
  localStorage.setItem(PLACEMENT_KEY, placement.value);
}

function startResize(event: MouseEvent) {
  event.preventDefault();
  const startX = event.clientX;
  const startY = event.clientY;
  const startSize = effectivePlacement.value === "bottom" ? bottomHeight.value : rightWidth.value;
  const workspace = dockRef.value?.parentElement;
  const workspaceRect = workspace?.getBoundingClientRect();

  const onMove = (moveEvent: MouseEvent) => {
    if (effectivePlacement.value === "bottom") {
      const max = Math.max(180, (workspaceRect?.height ?? window.innerHeight) - 120);
      bottomHeight.value = Math.round(
        Math.min(max, Math.max(160, startSize + startY - moveEvent.clientY)),
      );
    } else {
      const max = Math.max(320, (workspaceRect?.width ?? window.innerWidth) - 320);
      rightWidth.value = Math.round(
        Math.min(max, Math.max(280, startSize + startX - moveEvent.clientX)),
      );
    }
  };
  const onUp = () => {
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
    localStorage.setItem(BOTTOM_SIZE_KEY, String(bottomHeight.value));
    localStorage.setItem(RIGHT_SIZE_KEY, String(rightWidth.value));
    document.body.classList.remove("secondary-dock-resizing");
  };

  document.body.classList.add("secondary-dock-resizing");
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

function onNarrowChange(event: MediaQueryListEvent) {
  isNarrow.value = event.matches;
}

function onKeyDown(event: KeyboardEvent) {
  if (event.key === "Escape" && fullscreen.value) fullscreen.value = false;
}

onMounted(() => {
  narrowMq.addEventListener("change", onNarrowChange);
  document.addEventListener("keydown", onKeyDown);
});
onUnmounted(() => {
  narrowMq.removeEventListener("change", onNarrowChange);
  document.removeEventListener("keydown", onKeyDown);
});
</script>
