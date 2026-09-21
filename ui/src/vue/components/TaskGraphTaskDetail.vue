<template>
  <small :class="{ 'is-live': liveActivity }">{{ liveActivity || fallback }}</small>
</template>

<script setup lang="ts">
import { computed, watch } from "vue";
import { api } from "../../services/api";
import { messageStore } from "../../services/messageStore";
import type { TaskGraphTask } from "../../taskGraph";
import { useSubagentLive } from "../composables/subagentLive";

const props = defineProps<{
  task: TaskGraphTask;
  fallback: string;
}>();

const slug = computed(() => props.task.slug || "");
const conversationId = computed(() => props.task.child_conversation_id);
const { activity } = useSubagentLive(slug, conversationId);

const liveActivity = computed(() => {
  if (props.task.state !== "running" && props.task.state !== "starting") return "";
  return activity.value;
});

watch(
  () => [props.task.child_conversation_id, props.task.state],
  async () => {
    const id = props.task.child_conversation_id;
    if (!id || (props.task.state !== "running" && props.task.state !== "starting")) return;
    if (messageStore.peek(id)?.messages.length) return;
    try {
      messageStore.applyFullHistory(id, await api.getConversationWithProgress(id));
    } catch (error) {
      console.error("Failed to load task graph subagent activity:", error);
    }
  },
  { immediate: true },
);
</script>

<style scoped>
.is-live.is-live {
  display: block;
  overflow: hidden;
  max-width: 100%;
  color: var(--text-primary);
  font-family: var(--font-mono);
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
  -webkit-line-clamp: unset;
}

</style>
