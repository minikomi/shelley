<template>
  <section v-if="count > 0 || open" class="running-work" aria-label="Running work">
    <button
      type="button"
      class="running-work-toggle"
      data-testid="running-work-toggle"
      :aria-expanded="open"
      :aria-controls="`running-work-${conversationId}`"
      @click="toggleWork"
    >
      <span v-if="count > 0" class="running-work-dot" aria-hidden="true" />
      <span class="running-work-count">
        <span v-if="count === 0">No running work</span>
        <span v-if="jobCount > 0">{{ jobCount }} task{{ jobCount === 1 ? "" : "s" }}</span>
        <span
          v-if="jobCount > 0 && subagents.length > 0"
          class="running-work-separator"
          aria-hidden="true"
        />
        <span v-if="subagents.length > 0">
          {{ subagents.length }} subagent{{ subagents.length === 1 ? "" : "s" }}
        </span>
      </span>
      <span class="running-work-view">
        {{ open ? "Hide work" : "View work" }}
        <ToolChevron :expanded="open" />
      </span>
    </button>
    <div
      v-if="open"
      :id="`running-work-${conversationId}`"
      class="running-work-items"
      data-testid="running-work-items"
      role="region"
      aria-label="Running work details"
    >
      <RunningWorkAgent
        v-for="sub in seenSubagents"
        :key="sub.conversation_id"
        :conversation="sub"
        :finished="!runningSubagentIds.has(sub.conversation_id)"
      />
      <BackgroundJobsList v-if="jobCount > 0 || seenJobs.length > 0" :jobs="seenJobs" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, inject, ref, shallowRef, watch } from "vue";
import type { ConversationWithState } from "../../types";
import type { BackgroundJob } from "../../services/api";
import { BackgroundJobsKey } from "../composables/backgroundJobs";
import { ConversationsListKey } from "../composables/subagentLive";
import BackgroundJobsList from "./BackgroundJobsList.vue";
import RunningWorkAgent from "./RunningWorkAgent.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{ conversationId: string }>();
const emit = defineEmits<{ toggle: [] }>();

const conversations = inject(ConversationsListKey);
if (!conversations) throw new Error("RunningWorkStrip requires ConversationsListKey");
const backgroundJobs = inject(BackgroundJobsKey);
if (!backgroundJobs) throw new Error("RunningWorkStrip requires BackgroundJobsKey");

const open = ref(false);
const seenSubagents = shallowRef<ConversationWithState[]>([]);
const seenJobs = shallowRef<BackgroundJob[]>([]);

const jobCount = computed(
  () =>
    conversations.value.find((c) => c.conversation_id === props.conversationId)
      ?.running_background_jobs ?? 0,
);
const subagents = computed(() =>
  conversations.value.filter((c) => c.parent_conversation_id === props.conversationId && c.working),
);
const runningSubagentIds = computed(
  () => new Set(subagents.value.map((sub) => sub.conversation_id)),
);

const count = computed(() => jobCount.value + subagents.value.length);

function toggleWork() {
  open.value = !open.value;
  emit("toggle");
}

function remember<T>(seen: T[], current: T[], key: (item: T) => string): T[] {
  const items = new Map<string, T>();
  for (const item of [...seen, ...current]) items.set(key(item), item);
  return [...items.values()];
}

watch([open, subagents, backgroundJobs.jobs], ([expanded, agents, jobs]) => {
  if (!expanded) {
    seenSubagents.value = [];
    seenJobs.value = [];
    return;
  }
  seenSubagents.value = remember(seenSubagents.value, agents, (sub) => sub.conversation_id).sort(
    (a, b) =>
      a.created_at.localeCompare(b.created_at) ||
      a.conversation_id.localeCompare(b.conversation_id),
  );
  if (jobs !== null) {
    seenJobs.value = remember(seenJobs.value, jobs, (job) => job.job_id);
  }
});
</script>

<style scoped>
.running-work {
  flex: 0 0 auto;
  margin: 12px 0 0;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--bg-secondary);
  font-size: var(--font-size-13);
}

.running-work-toggle {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  padding: 11px 14px;
  border: 0;
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.running-work-toggle:hover {
  background: var(--bg-tertiary);
}

.running-work-dot {
  width: 7px;
  height: 7px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--primary);
}

.running-work-separator {
  width: 3px;
  height: 3px;
  flex-shrink: 0;
  border-radius: 50%;
  background: currentColor;
}

.running-work-count {
  display: flex;
  align-items: center;
  gap: 7px;
  font-weight: 500;
}

.running-work-view {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}

.running-work-items {
  border-top: 1px solid var(--border);
  background: var(--bg-base);
}
</style>
