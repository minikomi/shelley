<template>
  <TaskGraphView
    v-if="visibleGraph"
    :graph="visibleGraph"
    :collapsed="collapsed"
    variant="dock"
    @toggle="collapsed = !collapsed"
  />
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { api } from "../../services/api";
import type { TaskGraphSnapshot } from "../../taskGraph";
import { useFeatureFlag } from "../composables/featureFlags";
import TaskGraphView from "./TaskGraphView.vue";

const props = defineProps<{
  conversationId: string | null;
  refreshToken: number;
}>();

const enabled = useFeatureFlag("task-graph");
const graph = ref<TaskGraphSnapshot | null>(null);
const collapsed = ref(true);
let refreshTimer: number | null = null;
let requestID = 0;

const visibleGraph = computed(() => {
  if (!graph.value) return null;
  return graph.value.state === "complete" || graph.value.state === "cancelled" ? null : graph.value;
});

function clearRefreshTimer() {
  if (refreshTimer !== null) {
    window.clearTimeout(refreshTimer);
    refreshTimer = null;
  }
}

function scheduleRefresh() {
  clearRefreshTimer();
  if (graph.value?.state !== "active") return;
  refreshTimer = window.setTimeout(() => void loadGraph(), 1500);
}

async function loadGraph() {
  const conversationId = props.conversationId;
  const id = ++requestID;
  clearRefreshTimer();
  if (!conversationId || !enabled.value) {
    graph.value = null;
    return;
  }
  try {
    const next = await api.getLatestTaskGraph(conversationId);
    if (id !== requestID || conversationId !== props.conversationId) return;
    if (next?.id !== graph.value?.id) collapsed.value = true;
    graph.value = next;
  } catch (error) {
    if (id !== requestID) return;
    console.error("Failed to refresh task graph:", error);
  }
  scheduleRefresh();
}

watch(
  () => props.conversationId,
  () => {
    graph.value = null;
    collapsed.value = true;
    void loadGraph();
  },
);

watch(
  () => [props.refreshToken, enabled.value],
  () => void loadGraph(),
);

onMounted(() => void loadGraph());
onUnmounted(() => {
  requestID++;
  clearRefreshTimer();
});
</script>
