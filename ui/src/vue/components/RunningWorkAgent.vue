<template>
  <button
    type="button"
    class="running-work-agent"
    title="Open subagent conversation"
    @click="openAgent"
  >
    <span class="running-work-agent-description">
      <strong>{{ conversation.slug }}</strong>
      <span
        >Agent<template v-if="!finished"> · {{ activity || "Working…" }}</template></span
      >
      <span v-if="loadError" role="alert">{{ loadError }}</span>
    </span>
    <RunningToolTime
      v-if="!finished && startedAt"
      v-slot="{ elapsed }"
      class="running-work-agent-time"
      :start-time="startedAt"
    >
      Running · {{ elapsed }}
    </RunningToolTime>
    <span v-else-if="!finished" class="running-work-agent-time">Running</span>
    <span v-else class="running-work-agent-time">Finished</span>
    <span class="running-work-agent-open" aria-hidden="true">↗</span>
  </button>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import type { ConversationWithState } from "../../types";
import { navigateToConversationSlug, useSubagentLive } from "../composables/subagentLive";
import { api } from "../../services/api";
import { messageStore } from "../../services/messageStore";
import RunningToolTime from "./tools/RunningToolTime.vue";

const props = defineProps<{ conversation: ConversationWithState; finished: boolean }>();

const { activity, startedAt } = useSubagentLive(
  computed(() => props.conversation.slug ?? ""),
  computed(() => props.conversation.conversation_id),
);
const loadError = ref("");
onMounted(async () => {
  const id = props.conversation.conversation_id;
  try {
    await messageStore.hydrate(id);
    const cached = messageStore.peek(id);
    if (messageStore.needsBackfill(id)) {
      const response = await api.getConversationWithProgress(id);
      messageStore.applyFullHistory(id, response);
    } else if (
      cached &&
      (cached.needsRefresh || cached.maxSequenceIdKnown > cached.maxSequenceId)
    ) {
      const fromSeq = cached.maxSequenceId;
      const response = await api.getConversationSince(id, fromSeq);
      messageStore.applyIncrementalTail(id, response, fromSeq);
    }
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
});

function openAgent() {
  navigateToConversationSlug(props.conversation.slug ?? props.conversation.conversation_id);
}
</script>

<style scoped>
.running-work-agent {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 12px 14px;
  border: 0;
  border-bottom: 1px solid var(--border);
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  font-size: var(--font-size-13);
  text-align: left;
  cursor: pointer;
}

.running-work-agent:hover {
  background: var(--bg-secondary);
}

.running-work-agent-description {
  flex: 1;
  min-width: 0;
}

.running-work-agent-description strong {
  display: block;
  font-weight: 500;
}

.running-work-agent-description > span {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}

.running-work-agent-open,
.running-work-agent-time {
  color: var(--text-secondary);
  font-size: var(--font-size-12);
  white-space: nowrap;
}
</style>
