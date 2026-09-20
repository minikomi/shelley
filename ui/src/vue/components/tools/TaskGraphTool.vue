<template>
  <div v-if="visibleInTimeline && graph" class="tool" data-testid="tool-call-completed">
    <div class="tool-header" @click="collapsed = !collapsed">
      <div class="tool-summary">
        <span class="tool-emoji">◇</span>
        <span class="tool-name">task graph</span>
        <span class="tool-command">{{ graph.title || "Task graph" }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="collapsed ? 'Expand' : 'Collapse'"
        :aria-expanded="!collapsed"
      >
        <ToolChevron :expanded="!collapsed" />
      </button>
    </div>
    <div v-if="!collapsed" class="tool-details task-graph-tool-details">
      <TaskGraphView :graph="graph" :show-header="false" variant="inline" />
    </div>
  </div>
  <div v-else-if="visibleInTimeline" class="tool" data-testid="tool-call-completed">
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
import { computed, onMounted, onUnmounted, ref } from "vue";
import { api } from "../../../services/api";
import { taskGraphSnapshot, type TaskGraphSnapshot } from "../../../taskGraph";
import TaskGraphView from "../TaskGraphView.vue";
import ToolChevron from "./ToolChevron.vue";

const props = defineProps<{
  toolInput?: unknown;
  display?: unknown;
}>();

const collapsed = ref(true);
const graph = ref<TaskGraphSnapshot | null>(taskGraphSnapshot(props.display));
let refreshTimer: number | null = null;

const action = computed(() => {
  if (!props.toolInput || typeof props.toolInput !== "object") return "";
  const value = (props.toolInput as { action?: unknown }).action;
  return typeof value === "string" ? value : "";
});

const visibleInTimeline = computed(() => action.value === "" || action.value === "create");

const actionLabel = computed(() => {
  return action.value || "updated";
});

function clearRefreshTimer() {
  if (refreshTimer !== null) {
    window.clearTimeout(refreshTimer);
    refreshTimer = null;
  }
}

async function refreshGraph() {
  clearRefreshTimer();
  if (!visibleInTimeline.value) return;
  const current = graph.value;
  if (!current || current.state !== "active" || !current.parent_conversation_id) return;
  try {
    const latest = await api.getLatestTaskGraph(current.parent_conversation_id);
    if (!latest || latest.id !== current.id) return;
    graph.value = latest;
    if (latest.state === "active") {
      refreshTimer = window.setTimeout(() => void refreshGraph(), 1500);
    }
  } catch {
    // The persisted display snapshot remains usable if live refresh fails.
  }
}

onMounted(() => void refreshGraph());
onUnmounted(clearRefreshTimer);
</script>

<style scoped>
.task-graph-tool-details {
  padding: 0;
}
</style>
