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
      <span class="running-work-summary">
        <span v-if="!open && names.length" class="running-work-names">
          <template v-for="(name, index) in names" :key="index">
            <span v-if="index > 0" class="running-work-separator">,</span>
            <span class="running-work-name" :class="{ command: name.command }">
              {{ name.text }}
            </span>
          </template>
          <span v-if="count > names.length" class="running-work-more">
            and {{ count - names.length }} more
          </span>
        </span>
        <span v-else class="running-work-count">{{ counts }}</span>
        <span v-if="!open && names.length" class="running-work-activity">
          {{ singleJob ? lastOutput : singleSubagent ? agentActivity || "Working…" : counts }}
        </span>
      </span>
      <span class="running-work-view">
        <RunningToolTime v-if="!open && startedAt" v-slot="{ elapsed }" :start-time="startedAt">
          {{ elapsed }}
        </RunningToolTime>
        <span v-if="open">Hide work</span>
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
import { ConversationsListKey, useSubagentLive } from "../composables/subagentLive";
import BackgroundJobsList from "./BackgroundJobsList.vue";
import RunningWorkAgent from "./RunningWorkAgent.vue";
import ToolChevron from "./tools/ToolChevron.vue";
import RunningToolTime from "./tools/RunningToolTime.vue";

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
const counts = computed(() => {
  const parts = [];
  if (jobCount.value) parts.push(`${jobCount.value} task${jobCount.value === 1 ? "" : "s"}`);
  if (subagents.value.length) {
    parts.push(`${subagents.value.length} subagent${subagents.value.length === 1 ? "" : "s"}`);
  }
  return parts.join(" · ") || "No running work";
});
const singleJob = computed(() =>
  count.value === 1 && jobCount.value === 1 ? backgroundJobs.jobs.value?.[0] : undefined,
);
const singleSubagent = computed(() => (count.value === 1 ? subagents.value[0] : undefined));
const { activity: agentActivity, startedAt: agentStartedAt } = useSubagentLive(
  computed(() => singleSubagent.value?.slug ?? ""),
  computed(() => singleSubagent.value?.conversation_id),
);
const startedAt = computed(() => singleJob.value?.started_at ?? agentStartedAt.value);
const lastOutput = computed(
  () => singleJob.value?.tail.trim().split("\n").slice(-1)[0] || "No output yet.",
);
const names = computed(() =>
  [
    ...subagents.value.map((sub) => ({ text: sub.slug, command: false })),
    ...(backgroundJobs.jobs.value ?? []).map((job) => {
      const command = job.command.trim();
      return {
        text: command.split("\n")[0] + (command.includes("\n") ? " …" : ""),
        command: true,
      };
    }),
  ].slice(0, Math.min(2, count.value)),
);

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

.running-work-summary {
  flex: 1;
  min-width: 0;
}

.running-work-names {
  display: flex;
  align-items: baseline;
  gap: 4px;
  min-width: 0;
}

.running-work-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}

.running-work-name.command {
  max-width: 40ch;
  font-family: var(--font-mono);
  font-size: var(--font-size-12);
}

.running-work-activity {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}

.running-work-more {
  flex-shrink: 0;
}

.running-work-separator {
  flex-shrink: 0;
  margin-left: -4px;
  color: var(--text-secondary);
}

.running-work-dot {
  width: 7px;
  height: 7px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--primary);
}

.running-work-count {
  font-weight: 500;
}

.running-work-view {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
  flex-shrink: 0;
}

.running-work-items {
  border-top: 1px solid var(--border);
  background: var(--bg-base);
  max-height: min(420px, 50dvh);
  overflow: auto;
  overscroll-behavior: contain;
}
</style>
