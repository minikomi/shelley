<template>
  <TaskGraphView
    v-for="graph in activeGraphs"
    :key="graph.id"
    :graph="graph"
    :collapsed="!expanded.has(graph.id)"
    variant="dock"
    @toggle="toggle(graph.id)"
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
  discovering: boolean;
}>();

const enabled = useFeatureFlag("task-graph");
const graphs = ref<TaskGraphSnapshot[]>([]);
const expanded = ref(new Set<string>());
let refreshTimer: number | null = null;
let requestID = 0;

const activeGraphs = computed(() => graphs.value.filter((graph) => graph.state === "active"));

function toggle(id: string) {
  if (!expanded.value.delete(id)) expanded.value.add(id);
}

function clearRefreshTimer() {
  if (refreshTimer !== null) {
    window.clearTimeout(refreshTimer);
    refreshTimer = null;
  }
}

function scheduleRefresh() {
  clearRefreshTimer();
  if (activeGraphs.value.length === 0 && !props.discovering) return;
  refreshTimer = window.setTimeout(() => void loadGraphs(), 1500);
}

async function loadGraphs() {
  const conversationId = props.conversationId;
  const id = ++requestID;
  clearRefreshTimer();
  if (!conversationId || !enabled.value) {
    graphs.value = [];
    return;
  }
  try {
    const next = await api.getTaskGraphs(conversationId, { active: true });
    if (id !== requestID || conversationId !== props.conversationId) return;
    graphs.value = next;
  } catch (error) {
    if (id !== requestID) return;
    console.error("Failed to refresh task graphs:", error);
  }
  scheduleRefresh();
}

watch(
  () => props.conversationId,
  () => {
    graphs.value = [];
    expanded.value.clear();
    void loadGraphs();
  },
);

watch(
  () => [props.refreshToken, props.discovering, enabled.value],
  () => void loadGraphs(),
);

onMounted(() => void loadGraphs());
onUnmounted(() => {
  requestID++;
  clearRefreshTimer();
});
</script>
