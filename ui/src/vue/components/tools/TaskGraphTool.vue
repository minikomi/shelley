<template>
  <div
    v-if="visibleInTimeline && graph"
    class="tool"
    :data-testid="isRunning ? 'tool-call-running' : 'tool-call-completed'"
  >
    <div class="tool-header" @click="collapsed = !collapsed">
      <div class="tool-summary">
        <span class="tool-emoji">◇</span>
        <span class="tool-name">task graph</span>
        <span class="tool-command">{{ inlineLabel }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="collapsed ? 'Expand' : 'Collapse'"
        :aria-expanded="!collapsed"
      >
        <ToolChevron :expanded="!collapsed" />
      </button>
    </div>
    <div v-if="!collapsed" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">Tasks:</div>
        <TaskGraphView :graph="graph" :show-header="false" variant="inline" />
      </div>
    </div>
  </div>
  <div
    v-else-if="visibleInTimeline"
    class="tool"
    :data-testid="isRunning ? 'tool-call-running' : 'tool-call-completed'"
  >
    <div class="tool-header">
      <div class="tool-summary">
        <span class="tool-emoji">◇</span>
        <span class="tool-name">task graph</span>
        <span class="tool-command">{{ actionLabel }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "../../../services/api";
import { taskGraphSnapshot, type TaskGraphSnapshot } from "../../../taskGraph";
import { CurrentConversationIdKey } from "../../composables/subagentLive";
import TaskGraphView from "../TaskGraphView.vue";
import ToolChevron from "./ToolChevron.vue";

const props = defineProps<{
  toolInput?: unknown;
  display?: unknown;
  isRunning?: boolean;
}>();

const collapsed = ref(true);
const graph = ref<TaskGraphSnapshot | null>(taskGraphSnapshot(props.display));
const currentConversationId = inject(CurrentConversationIdKey, null);
let refreshTimer: number | null = null;
let requestID = 0;

const action = computed(() => {
  if (!props.toolInput || typeof props.toolInput !== "object") return "";
  const value = (props.toolInput as { action?: unknown }).action;
  return typeof value === "string" ? value : "";
});

const visibleInTimeline = computed(() => action.value === "" || action.value === "create");

const requestedTitle = computed(() => {
  if (!props.toolInput || typeof props.toolInput !== "object") return "";
  const value = (props.toolInput as { title?: unknown }).title;
  return typeof value === "string" ? value : "";
});

const actionLabel = computed(() => {
  return requestedTitle.value || action.value || "updated";
});

const inlineLabel = computed(() => {
  const current = graph.value;
  if (!current) return `${props.isRunning ? "starting · " : ""}${actionLabel.value}`;
  const complete = current.tasks.filter((task) => task.state === "complete").length;
  const running = current.tasks.filter(
    (task) => task.state === "running" || task.state === "starting",
  ).length;
  const parts = [];
  if (running) parts.push(`${running} running`);
  if (complete) parts.push(`${complete}/${current.tasks.length} complete`);
  return [...parts, current.title || "Task graph"].join(" · ");
});

function clearRefreshTimer() {
  if (refreshTimer !== null) {
    window.clearTimeout(refreshTimer);
    refreshTimer = null;
  }
}

function scheduleRefresh() {
  clearRefreshTimer();
  refreshTimer = window.setTimeout(() => void refreshGraph(), 1500);
}

async function refreshGraph() {
  const id = ++requestID;
  clearRefreshTimer();
  if (!visibleInTimeline.value) return;
  const current = graph.value;
  if (current && current.state !== "active") return;
  const parentConversationId = current?.parent_conversation_id || currentConversationId?.value;
  if (!parentConversationId) return;
  try {
    const graphs = await api.getTaskGraphs(parentConversationId);
    if (id !== requestID) return;
    const latest = current
      ? graphs.find((candidate) => candidate.id === current.id)
      : [...graphs].reverse().find((candidate) => candidate.title === requestedTitle.value);
    if (!latest) {
      if (props.isRunning) scheduleRefresh();
      return;
    }
    graph.value = latest;
    if (latest.state === "active") {
      scheduleRefresh();
    }
  } catch (error) {
    if (id !== requestID) return;
    console.error("Failed to refresh inline task graph:", error);
    if (props.isRunning || current?.state === "active") scheduleRefresh();
  }
}

watch(
  () => props.display,
  (display) => {
    const snapshot = taskGraphSnapshot(display);
    if (!snapshot) return;
    graph.value = snapshot;
    void refreshGraph();
  },
);

onMounted(() => void refreshGraph());
onUnmounted(() => {
  requestID++;
  clearRefreshTimer();
});
</script>
