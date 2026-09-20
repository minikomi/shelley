<template>
  <div
    class="task-graph"
    :class="[`task-graph-${variant}`, `task-graph-${tone}`, { collapsed }]"
    data-testid="task-graph"
  >
    <button
      type="button"
      class="task-graph-header"
      :aria-expanded="!collapsed"
      @click="emit('toggle')"
    >
      <span class="task-graph-indicator" aria-hidden="true">{{ indicator }}</span>
      <span class="task-graph-heading">
        <strong>{{ graph.title || "Task graph" }}</strong>
        <small>{{ summary }}</small>
      </span>
      <svg
        class="task-graph-chevron"
        :class="{ expanded: !collapsed }"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
        aria-hidden="true"
      >
        <path stroke-linecap="round" stroke-linejoin="round" :stroke-width="2" d="M6 9l6 6 6-6" />
      </svg>
    </button>

    <div v-if="!collapsed" class="task-graph-rows">
      <div
        v-for="(task, index) in graph.tasks"
        :key="task.id"
        class="task-graph-row"
        :class="`task-state-${task.state}`"
      >
        <span class="task-state-icon" aria-hidden="true">{{ taskIcon(task, index) }}</span>
        <span class="task-graph-copy">
          <strong>{{ task.title }}</strong>
          <small>{{ taskDetail(task) }}</small>
          <span v-if="task.state === 'running'" class="task-progress" aria-hidden="true">
            <span />
          </span>
        </span>
        <button
          v-if="task.slug"
          type="button"
          class="task-graph-action"
          @click.stop="openTask(task.slug)"
        >
          Open
        </button>
        <span v-else class="task-graph-state-label">{{ stateLabel(task.state) }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { TaskGraphSnapshot, TaskGraphTask, TaskState } from "../../taskGraph";
import { navigateToConversationSlug } from "../composables/subagentLive";

const props = withDefaults(
  defineProps<{
    graph: TaskGraphSnapshot;
    collapsed?: boolean;
    variant?: "dock" | "inline";
  }>(),
  {
    collapsed: false,
    variant: "dock",
  },
);

const emit = defineEmits<{ toggle: [] }>();

const counts = computed(() => {
  const tasks = props.graph.tasks;
  return {
    complete: tasks.filter((task) => task.state === "complete").length,
    running: tasks.filter((task) => task.state === "running" || task.state === "starting").length,
    blocked: tasks.filter(
      (task) =>
        task.state === "pending" &&
        (task.depends_on || []).some(
          (dependency) =>
            props.graph.tasks.find((candidate) => candidate.id === dependency)?.state !==
            "complete",
        ),
    ).length,
    failed: tasks.filter((task) => task.state === "failed").length,
    cancelled: tasks.filter((task) => task.state === "cancelled").length,
  };
});

const tone = computed(() => {
  if (props.graph.state === "failed" || counts.value.failed) return "failed";
  if (props.graph.state === "complete") return "complete";
  if (counts.value.running) return "running";
  return "idle";
});

const indicator = computed(() => {
  if (tone.value === "complete") return "✓";
  if (tone.value === "failed") return "!";
  if (tone.value === "running") return "•";
  return props.graph.tasks.length.toString();
});

const summary = computed(() => {
  const parts = [`${counts.value.complete}/${props.graph.tasks.length} complete`];
  if (counts.value.running) parts.push(`${counts.value.running} running`);
  if (counts.value.blocked) parts.push(`${counts.value.blocked} blocked`);
  if (counts.value.failed) parts.push(`${counts.value.failed} failed`);
  if (counts.value.cancelled) parts.push(`${counts.value.cancelled} cancelled`);
  return parts.join(" · ");
});

function stateLabel(state: TaskState): string {
  switch (state) {
    case "complete":
      return "Done";
    case "failed":
      return "Failed";
    case "cancelled":
      return "Cancelled";
    case "running":
    case "starting":
      return "Running";
    case "ready":
      return "Ready";
    default:
      return "Blocked";
  }
}

function taskIcon(task: TaskGraphTask, index: number): string {
  if (task.state === "complete") return "✓";
  if (task.state === "failed") return "!";
  if (task.state === "running" || task.state === "starting") return "•";
  return String(index + 1);
}

function taskDetail(task: TaskGraphTask): string {
  if (task.error) return task.error;
  if (task.state === "complete" && task.result) return task.result.split("\n")[0];
  if (task.state === "running" || task.state === "starting") {
    return [task.model, task.owner].filter(Boolean).join(" · ");
  }
  if (task.state === "pending" && task.depends_on?.length) {
    const dependencies = task.depends_on
      .map((id) => props.graph.tasks.find((candidate) => candidate.id === id)?.title || id)
      .join(", ");
    return `Waiting for ${dependencies}`;
  }
  return task.owner === "parent" ? "Parent task" : task.model || "Subagent task";
}

function openTask(slug: string) {
  navigateToConversationSlug(slug);
}
</script>

<style scoped>
.task-graph {
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 0.625rem;
  background: var(--bg-secondary);
  color: var(--text-primary);
}

.task-graph-dock {
  margin: 0 1rem;
  box-shadow: 0 -0.5rem 1.5rem rgb(0 0 0 / 18%);
}

.task-graph-running {
  border-color: color-mix(in srgb, var(--accent-primary) 55%, var(--border));
}

.task-graph-complete {
  border-color: color-mix(in srgb, var(--success-text) 35%, var(--border));
}

.task-graph-failed {
  border-color: var(--error-border);
}

.task-graph-header {
  display: flex;
  align-items: center;
  gap: 0.625rem;
  width: 100%;
  min-height: 3rem;
  padding: 0.5rem 0.75rem;
  border: 0;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
}

.task-graph-indicator,
.task-state-icon {
  display: grid;
  place-items: center;
  width: 1.35rem;
  height: 1.35rem;
  flex: none;
  border-radius: 50%;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
  font-size: 0.6875rem;
  font-weight: 700;
}

.task-graph-running .task-graph-indicator,
.task-state-running .task-state-icon,
.task-state-starting .task-state-icon {
  background: var(--accent-primary);
  color: white;
  box-shadow: 0 0 0 0.25rem color-mix(in srgb, var(--accent-primary) 18%, transparent);
}

.task-graph-complete .task-graph-indicator,
.task-state-complete .task-state-icon {
  background: var(--success-bg);
  color: var(--success-text);
}

.task-graph-failed .task-graph-indicator,
.task-state-failed .task-state-icon {
  background: var(--error-bg);
  color: var(--error-text);
}

.task-graph-heading,
.task-graph-copy {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
}

.task-graph-heading strong,
.task-graph-copy strong {
  overflow: hidden;
  font-size: 0.8125rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-graph-heading small,
.task-graph-copy small {
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 0.6875rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-graph-chevron {
  width: 1rem;
  height: 1rem;
  flex: none;
  color: var(--text-secondary);
  transition: transform 0.15s ease;
}

.task-graph-chevron.expanded {
  transform: rotate(180deg);
}

.task-graph-rows {
  max-height: 15rem;
  overflow-y: auto;
  border-top: 1px solid var(--border);
}

.task-graph-row {
  display: grid;
  grid-template-columns: 1.35rem minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.625rem;
  min-height: 2.75rem;
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.task-graph-row:last-child {
  border-bottom: 0;
}

.task-state-running,
.task-state-starting {
  background: color-mix(in srgb, var(--accent-primary) 6%, transparent);
}

.task-state-failed {
  background: color-mix(in srgb, var(--error-bg) 55%, transparent);
}

.task-progress {
  width: min(9rem, 55%);
  height: 0.1875rem;
  margin-top: 0.375rem;
  overflow: hidden;
  border-radius: 1rem;
  background: var(--bg-tertiary);
}

.task-progress span {
  display: block;
  width: 62%;
  height: 100%;
  border-radius: inherit;
  background: var(--accent-primary);
  animation: task-progress-pulse 1.4s ease-in-out infinite alternate;
}

.task-graph-action {
  padding: 0.25rem 0.5rem;
  border: 1px solid var(--border);
  border-radius: 0.375rem;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
  font-size: 0.6875rem;
  cursor: pointer;
}

.task-graph-state-label {
  color: var(--text-tertiary);
  font-size: 0.6875rem;
}

.task-graph-inline {
  margin: 0.5rem 0;
}

.task-graph-inline.collapsed .task-graph-header {
  min-height: 2.75rem;
}

@keyframes task-progress-pulse {
  from {
    opacity: 0.55;
    transform: translateX(-12%);
  }
  to {
    opacity: 1;
    transform: translateX(28%);
  }
}

@media (max-width: 600px) {
  .task-graph-dock {
    margin: 0 0.75rem;
  }

  .task-graph-header,
  .task-graph-row {
    padding-right: 0.625rem;
    padding-left: 0.625rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .task-progress span {
    animation: none;
  }
}
</style>
