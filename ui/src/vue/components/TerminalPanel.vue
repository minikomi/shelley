<!-- Vue port of components/TerminalPanel.tsx. A bottom dock of ephemeral
     terminals backed by server-side dtach sessions (so they survive
     conversation switches + reloads). Preserves the .terminal-panel* class
     contract, the action-button titles, and the tab status indicators.

     The EphemeralTerminal type is re-exported here (from terminalTypes.ts) so
     other code can `import type { EphemeralTerminal } from
     "./components/TerminalPanel.vue"` exactly as it imported from the React
     module. The actual xterm.js + websocket lifecycle lives in the
     TerminalInstance.vue child (one per terminal).

     React callback props are mapped to emits:
       onClose                -> emit("close", id)
       onInsertIntoInput      -> emit("insert-into-input", text)
       onAutoFocusConsumed    -> emit("auto-focus-consumed")
       onActiveTerminalExited -> emit("active-terminal-exited")
       onAttached             -> emit("attached", id, termId)
     The presence of an onInsertIntoInput handler in React (which gates the
     insert buttons) is mirrored by the required `canInsertIntoInput` prop. -->
<template>
  <div
    v-show="visible.length > 0"
    :class="`terminal-panel${minimized ? ' terminal-panel-minimized' : ''}`"
    :style="minimized ? undefined : { height: `${height}px`, flexShrink: 0 }"
  >
    <!-- Resize handle at top — hidden when minimized -->
    <div v-if="!minimized" class="terminal-panel-resize-handle" @mousedown="handleResizeMouseDown">
      <div class="terminal-panel-resize-grip" />
    </div>

    <!-- Tab bar + actions -->
    <div class="terminal-panel-header">
      <!-- Minimize/maximize toggle -->
      <button
        v-tooltip.top="minimized ? 'Expand terminals' : 'Minimize terminals'"
        class="terminal-panel-action-btn"
        :aria-label="minimized ? 'Expand terminals' : 'Minimize terminals'"
        @click="toggleMinimized"
      >
        <ChevronUpIcon v-if="minimized" />
        <ChevronDownIcon v-else />
      </button>

      <div class="terminal-panel-tabs">
        <template v-for="(t, i) in visible" :key="t.id">
          <!-- Separator between global terminals and the ones pinned to this
               conversation. -->
          <div
            v-if="i > 0 && t.conversationId !== null && visible[i - 1].conversationId === null"
            class="terminal-panel-tabs-divider"
          />
          <div
            :class="`terminal-panel-tab${t.id === activeTabId ? ' terminal-panel-tab-active' : ''}`"
            :title="t.command"
            @click="onTabClick(t.id)"
          >
            <span
              v-if="statusMap.get(t.id)?.status === 'running'"
              class="terminal-panel-tab-indicator terminal-panel-tab-running"
              >●</span
            >
            <span
              v-if="statusMap.get(t.id)?.status === 'exited' && statusMap.get(t.id)?.exitCode === 0"
              class="terminal-panel-tab-indicator terminal-panel-tab-success"
              >✓</span
            >
            <span
              v-if="statusMap.get(t.id)?.status === 'exited' && statusMap.get(t.id)?.exitCode !== 0"
              class="terminal-panel-tab-indicator terminal-panel-tab-error"
              >✗</span
            >
            <span
              v-if="statusMap.get(t.id)?.status === 'error'"
              class="terminal-panel-tab-indicator terminal-panel-tab-error"
              >✗</span
            >
            <GlobeIcon
              v-if="t.conversationId === null && isAlive(t.id)"
              class="terminal-panel-tab-globe"
              aria-label="Shown in all conversations"
            />
            <span class="terminal-panel-tab-label">{{ tabLabel(t.command) }}</span>
            <button
              v-tooltip.top="'Close terminal'"
              class="terminal-panel-tab-close"
              aria-label="Close terminal"
              @click.stop="emit('close', t.id)"
            >
              ×
            </button>
          </div>
        </template>
      </div>

      <!-- Action buttons — hidden when minimized -->
      <div v-if="!minimized" class="terminal-panel-actions">
        <button
          v-tooltip.top="'Copy visible screen'"
          :class="`terminal-panel-action-btn${copyFeedback === 'copyScreen' ? ' terminal-panel-action-btn-feedback' : ''}`"
          aria-label="Copy visible screen"
          @click="copyScreen"
        >
          <CheckIcon v-if="copyFeedback === 'copyScreen'" />
          <CopyIcon v-else />
        </button>
        <button
          v-tooltip.top="'Copy all output'"
          :class="`terminal-panel-action-btn${copyFeedback === 'copyAll' ? ' terminal-panel-action-btn-feedback' : ''}`"
          aria-label="Copy all output"
          @click="copyAll"
        >
          <CheckIcon v-if="copyFeedback === 'copyAll'" />
          <CopyAllIcon v-else />
        </button>
        <template v-if="canInsertIntoInput">
          <button
            v-tooltip.top="'Insert visible screen into input'"
            :class="`terminal-panel-action-btn${copyFeedback === 'insertScreen' ? ' terminal-panel-action-btn-feedback' : ''}`"
            aria-label="Insert visible screen into input"
            @click="insertScreen"
          >
            <CheckIcon v-if="copyFeedback === 'insertScreen'" />
            <InsertIcon v-else />
          </button>
          <button
            v-tooltip.top="'Insert all output into input'"
            :class="`terminal-panel-action-btn${copyFeedback === 'insertAll' ? ' terminal-panel-action-btn-feedback' : ''}`"
            aria-label="Insert all output into input"
            @click="insertAll"
          >
            <CheckIcon v-if="copyFeedback === 'insertAll'" />
            <InsertAllIcon v-else />
          </button>
        </template>
        <div class="terminal-panel-actions-divider" />
        <!-- Scope toggle for the active terminal. Hidden entirely when the
             terminal has exited or errored, since the server has already
             forgotten the session and a scope PUT would 404. The tooltip lives
             on a wrapper because a disabled button does not emit hover events. -->
        <template v-if="activeTerminal && isAlive(activeTerminal.id)">
          <span v-tooltip.top="scopeTooltip">
            <button
              class="terminal-panel-action-btn"
              :disabled="scopeDisabled"
              :aria-label="scopeTooltip"
              @click="toggleActiveScope"
            >
              <PinIcon v-if="activeTerminal?.conversationId === null" />
              <GlobeIcon v-else />
            </button>
          </span>
          <div class="terminal-panel-actions-divider" />
        </template>
        <button
          v-tooltip.top="'Close active terminal'"
          class="terminal-panel-action-btn"
          aria-label="Close active terminal"
          @click="handleCloseActive"
        >
          <CloseIcon />
        </button>
      </div>
    </div>

    <!-- Terminal content area — hidden (not unmounted) when minimized -->
    <div class="terminal-panel-content" :style="minimized ? { display: 'none' } : undefined">
      <TerminalInstance
        v-for="t in terminals"
        :key="t.id"
        :term="t"
        :is-visible="t.id === activeTabId"
        :is-dark="isDark"
        :model="model ?? null"
        @status-change="handleStatusChange"
        @register="registerXterm"
        @unregister="unregisterXterm"
        @attached="(id, termId) => emit('attached', id, termId)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import type { Terminal } from "@xterm/xterm";
import { isDarkModeActive } from "../../services/theme";
import TerminalInstance from "./TerminalInstance.vue";
import type { TermStatus } from "./terminalHelpers";
import { visibleTerminals } from "./terminalHelpers";
import type { EphemeralTerminal } from "./terminalTypes";
import CopyIcon from "./terminalIcons/CopyIcon.vue";
import CopyAllIcon from "./terminalIcons/CopyAllIcon.vue";
import InsertIcon from "./terminalIcons/InsertIcon.vue";
import InsertAllIcon from "./terminalIcons/InsertAllIcon.vue";
import CheckIcon from "./terminalIcons/CheckIcon.vue";
import CloseIcon from "./terminalIcons/CloseIcon.vue";
import ChevronUpIcon from "./terminalIcons/ChevronUpIcon.vue";
import ChevronDownIcon from "./terminalIcons/ChevronDownIcon.vue";
import GlobeIcon from "./terminalIcons/GlobeIcon.vue";
import PinIcon from "./terminalIcons/PinIcon.vue";

// Re-export EphemeralTerminal so importers can keep importing it from this
// module (the canonical definition lives in terminalTypes.ts).
export type { EphemeralTerminal } from "./terminalTypes";

const props = defineProps<{
  terminals: EphemeralTerminal[];
  autoFocusId?: string | null;
  // Mirrors the presence of React's onInsertIntoInput callback, which gates
  // the insert buttons. When false the insert actions are not rendered.
  canInsertIntoInput?: boolean;
  // Context surfaced to spawned sessions via SHELLEY_* env vars. Only used on
  // initial spawn; reattaches use the env baked in when the session was
  // created.
  conversationId?: string | null;
  model?: string | null;
}>();

const emit = defineEmits<{
  (e: "close", id: string): void;
  (e: "insert-into-input", text: string): void;
  (e: "auto-focus-consumed"): void;
  (e: "active-terminal-exited"): void;
  (e: "attached", id: string, termId: string): void;
  // scope-change: terminal id, new owner (null for global). Emitted only after
  // the server has accepted the change.
  (e: "scope-change", id: string, conversationId: string | null): void;
  (e: "scope-error", message: string): void;
}>();

const activeTabId = ref<string | null>(null);
const height = ref(300);
const minimized = ref(false);
const copyFeedback = ref<string | null>(null);
const statusMap = ref<Map<string, { status: TermStatus; exitCode: number | null }>>(new Map());
// Terminals to offer as tabs here: this conversation's own, then the global
// ones. Every terminal stays mounted regardless; this only drives what is
// reachable from the tab bar.
const visible = computed(() => visibleTerminals(props.terminals, props.conversationId ?? null));

const activeTerminal = computed(
  () => visible.value.find((t) => t.id === activeTabId.value) ?? null,
);

// True while a scope request is in flight, so the button cannot be
// double-submitted.
const scopePending = ref(false);

// A terminal is alive if the server hasn't reported it as exited or errored.
// Dead terminals have no server-side record to scope against.
function isAlive(id: string): boolean {
  const s = statusMap.value.get(id)?.status;
  return s !== "exited" && s !== "error";
}

// The global -> local direction needs a conversation to move the terminal
// into, which /new does not have. A dead terminal can't be scoped at all.
const scopeDisabled = computed(() => {
  if (scopePending.value || !activeTerminal.value) return true;
  if (!isAlive(activeTerminal.value.id)) return true;
  return activeTerminal.value.conversationId === null && !props.conversationId;
});

const scopeTooltip = computed(() => {
  const active = activeTerminal.value;
  if (!active) return "No active terminal";
  if (active.conversationId !== null) return "Show in all conversations";
  if (!props.conversationId) return "Open a conversation to make this terminal local";
  return "Keep in this conversation";
});

async function toggleActiveScope() {
  const active = activeTerminal.value;
  if (!active || scopeDisabled.value) return;
  // Without a server-side session there is nothing to persist against yet.
  if (!active.termId) {
    emit("scope-error", "This terminal is still starting up.");
    return;
  }
  const next = active.conversationId === null ? (props.conversationId ?? null) : null;
  scopePending.value = true;
  try {
    const res = await fetch(`/api/terminals/${encodeURIComponent(active.termId)}/scope`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ conversation_id: next }),
    });
    if (!res.ok) {
      throw new Error((await res.text()).trim() || `Request failed with status ${res.status}`);
    }
    emit("scope-change", active.id, next);
  } catch (err) {
    emit("scope-error", err instanceof Error ? err.message : "Failed to change terminal scope");
  } finally {
    scopePending.value = false;
  }
}

const isResizingRef = { current: false };
const startYRef = { current: 0 };
const startHeightRef = { current: 0 };

// Detect dark mode
const isDark = ref(isDarkModeActive());
let observer: MutationObserver | null = null;
onMounted(() => {
  observer = new MutationObserver(() => {
    isDark.value = isDarkModeActive();
  });
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  });
});
onUnmounted(() => observer?.disconnect());

// Auto-select the newest visible tab whenever the set of visible terminals
// changes. Keyed on the ids rather than the count: two conversations can hold
// the same number of different terminals, and a count watcher would then leave
// activeTabId pointing at a terminal that is not shown here.
// Track which terminals were visible last time so a newly created one can be
// auto-selected while an existing selection is otherwise left alone.
let prevVisibleIds: string[] = [];
watch(
  () => visible.value.map((t) => t.id).join("\u0000"),
  () => {
    const list = visible.value;
    const seen = new Set(prevVisibleIds);
    prevVisibleIds = list.map((t) => t.id);
    if (list.length === 0) {
      activeTabId.value = null;
      return;
    }
    // A terminal that just appeared takes focus; this is how newly created
    // terminals get selected.
    const added = list.filter((t) => !seen.has(t.id));
    if (added.length > 0) {
      activeTabId.value = added[added.length - 1].id;
      minimized.value = false; // expand when a new terminal arrives
      return;
    }
    // Otherwise keep the current tab if it is still visible, else fall back to
    // the most recent one.
    if (activeTabId.value && list.some((t) => t.id === activeTabId.value)) return;
    activeTabId.value = list[list.length - 1].id;
  },
  { immediate: true },
);

function handleStatusChange(id: string, status: TermStatus, exitCode: number | null) {
  const prev = statusMap.value;
  const next = new Map(prev);
  const existing = next.get(id);
  // Don't overwrite exit status with ws.onclose
  if (existing && existing.status === "exited" && status === "exited") {
    return;
  }
  next.set(id, {
    status,
    exitCode: exitCode ?? existing?.exitCode ?? null,
  });
  statusMap.value = next;
}

// Resize drag
function handleResizeMouseDown(e: MouseEvent) {
  e.preventDefault();
  isResizingRef.current = true;
  startYRef.current = e.clientY;
  startHeightRef.current = height.value;

  const handleMouseMove = (ev: MouseEvent) => {
    if (!isResizingRef.current) return;
    // Dragging up increases height
    const delta = startYRef.current - ev.clientY;
    height.value = Math.max(80, Math.min(800, startHeightRef.current + delta));
  };

  const handleMouseUp = () => {
    isResizingRef.current = false;
    document.removeEventListener("mousemove", handleMouseMove);
    document.removeEventListener("mouseup", handleMouseUp);
  };

  document.addEventListener("mousemove", handleMouseMove);
  document.addEventListener("mouseup", handleMouseUp);
}

function showFeedback(type: string) {
  copyFeedback.value = type;
  setTimeout(() => (copyFeedback.value = null), 1500);
}

// Registry of xterm instances by terminal id.
const xtermRegistry = new Map<string, Terminal>();
function registerXterm(id: string, xterm: Terminal) {
  xtermRegistry.set(id, xterm);
}
function unregisterXterm(id: string) {
  xtermRegistry.delete(id);
}

// Auto-focus terminal when autoFocusId is set (React effect on
// [autoFocusId, onAutoFocusConsumed]).
watch(
  () => props.autoFocusId,
  (autoFocusId) => {
    if (!autoFocusId) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    let attempt = 0;
    const tryFocus = () => {
      if (cancelled) return;
      const xterm = xtermRegistry.get(autoFocusId);
      if (xterm) {
        activeTabId.value = autoFocusId;
        minimized.value = false; // expand when focusing a terminal
        // Double-rAF to ensure we're past any keyup/form events that might steal focus
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            xterm.focus();
          });
        });
        emit("auto-focus-consumed");
        return;
      }
      if (++attempt < 10) {
        timer = setTimeout(tryFocus, 50);
      }
    };
    // Small initial delay to let the form submit / keyup events settle
    timer = setTimeout(tryFocus, 50);
    // Cleanup when autoFocusId changes again.
    const stop = watch(
      () => props.autoFocusId,
      () => {
        cancelled = true;
        clearTimeout(timer);
        stop();
      },
    );
  },
);

// Restore focus to message input when the active terminal exits (React effect
// on [activeTabId, statusMap, onActiveTerminalExited]).
const prevActiveStatusRef = {
  current: { tabId: null as string | null, status: undefined as TermStatus | undefined },
};
watch(
  () => [activeTabId.value, statusMap.value] as const,
  () => {
    if (!activeTabId.value) return;
    const info = statusMap.value.get(activeTabId.value);
    const prev = prevActiveStatusRef.current;
    // Only trigger on status transition within the same tab
    const wasRunning = prev.tabId === activeTabId.value && prev.status === "running";
    prevActiveStatusRef.current = { tabId: activeTabId.value, status: info?.status };
    if (wasRunning && (info?.status === "exited" || info?.status === "error")) {
      emit("active-terminal-exited");
    }
  },
);

function getBufferText(mode: "screen" | "all"): string {
  if (!activeTabId.value) return "";
  const xterm = xtermRegistry.get(activeTabId.value);
  if (!xterm) return "";

  const lines: string[] = [];
  const buffer = xterm.buffer.active;

  if (mode === "screen") {
    const startRow = buffer.viewportY;
    for (let i = 0; i < xterm.rows; i++) {
      const line = buffer.getLine(startRow + i);
      if (line) lines.push(line.translateToString(true));
    }
  } else {
    for (let i = 0; i < buffer.length; i++) {
      const line = buffer.getLine(i);
      if (line) lines.push(line.translateToString(true));
    }
  }
  return lines.join("\n").trimEnd();
}

function copyScreen() {
  navigator.clipboard.writeText(getBufferText("screen"));
  showFeedback("copyScreen");
}
function copyAll() {
  navigator.clipboard.writeText(getBufferText("all"));
  showFeedback("copyAll");
}
function insertScreen() {
  if (props.canInsertIntoInput) {
    emit("insert-into-input", getBufferText("screen"));
    showFeedback("insertScreen");
  }
}
function insertAll() {
  if (props.canInsertIntoInput) {
    emit("insert-into-input", getBufferText("all"));
    showFeedback("insertAll");
  }
}

function handleCloseActive() {
  if (activeTabId.value) emit("close", activeTabId.value);
}

function toggleMinimized() {
  minimized.value = !minimized.value;
}

function onTabClick(id: string) {
  activeTabId.value = id;
  if (minimized.value) minimized.value = false;
}

// Refit terminals when un-minimizing by nudging the container to trigger
// ResizeObserver (React effect on [minimized, activeTabId]).
const wasMinimizedRef = { current: minimized.value };
watch(
  () => [minimized.value, activeTabId.value] as const,
  () => {
    const wasMinimized = wasMinimizedRef.current;
    wasMinimizedRef.current = minimized.value;
    if (wasMinimized && !minimized.value && activeTabId.value) {
      const timer = setTimeout(() => {
        const el = document.querySelector(`[data-terminal-id="${activeTabId.value}"]`);
        if (el) {
          (el as HTMLElement).style.height = "99.9%";
          requestAnimationFrame(() => {
            (el as HTMLElement).style.height = "100%";
          });
        }
      }, 30);
      // No explicit cleanup needed; the timer is short-lived.
      void timer;
    }
  },
);

// Truncate command for tab label
function tabLabel(cmd: string): string {
  // Show first word or first 30 chars
  const firstWord = cmd.split(/\s+/)[0];
  if (firstWord.length > 30) return firstWord.substring(0, 27) + "...";
  return firstWord;
}
</script>
