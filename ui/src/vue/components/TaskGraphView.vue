<template>
  <div
    class="task-graph"
    :class="[`task-graph-${variant}`, `task-graph-${tone}`, { collapsed }]"
    data-testid="task-graph"
  >
    <button
      v-if="showHeader"
      type="button"
      class="task-graph-header"
      :aria-expanded="!collapsed"
      @click="emit('toggle')"
    >
      <span class="task-graph-indicator" aria-hidden="true">{{ indicator }}</span>
      <span class="task-graph-heading">
        <strong v-if="variant !== 'dock' || !collapsed">{{ graph.title || "Task graph" }}</strong>
        <small>{{ variant === "dock" && collapsed ? dockSummary : summary }}</small>
      </span>
      <svg
        class="task-graph-chevron"
        :class="{ expanded: !collapsed }"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
        aria-hidden="true"
      >
        <path stroke-linecap="round" stroke-linejoin="round" :stroke-width="2" d="m9 6 6 6-6 6" />
      </svg>
    </button>

    <p v-if="!collapsed && graph.serial_rationale" class="task-graph-rationale">
      {{ graph.serial_rationale }}
    </p>

    <div v-if="!collapsed && topology.layered" class="task-graph-rows task-graph-flow">
      <template v-for="(layer, layerIndex) in topology.layers" :key="layer.depth">
        <div
          class="task-flow-layer"
          :class="{ 'is-group': layer.tasks.length > 1, 'is-single': layer.tasks.length === 1 }"
        >
          <div
            v-for="{ task, index } in layer.tasks"
            :key="task.id"
            class="task-graph-row"
            :class="`task-state-${task.state}`"
          >
            <span class="task-state-icon" aria-hidden="true">{{ taskIcon(task, index) }}</span>
            <span class="task-graph-copy">
              <button type="button" class="task-graph-title" @click="toggleBrief(task.id)">
                <strong>{{ task.title }}</strong>
              </button>
              <TaskGraphTaskDetail :task="task" :fallback="taskDetail(task)" />
              <TaskGraphTaskBrief v-if="briefs.has(task.id)" :task="task" />
              <span v-if="task.state === 'running'" class="task-progress" aria-hidden="true">
                <span />
              </span>
            </span>
            <button
              v-if="task.slug && task.child_conversation_id"
              type="button"
              class="task-graph-action"
              @click.stop="openTask(task.slug)"
            >
              <TaskGraphRunningLabel v-if="task.state === 'running'" />
              <template v-else>{{ stateLabel(task.state) }}</template>
              <TaskGraphElapsedTime
                v-if="task.state === 'running' || task.state === 'starting'"
                :started-at="task.started_at"
              />
            </button>
            <span v-else class="task-graph-state-label">
              {{ stateLabel(task.state) }}
              <TaskGraphElapsedTime
                v-if="task.state === 'running' || task.state === 'starting'"
                :started-at="task.started_at"
              />
            </span>
          </div>
        </div>
        <div
          v-if="layerIndex < topology.layers.length - 1"
          class="task-flow-junction"
          :class="{ 'is-finished': layer.tasks.every(({ task }) => task.state === 'complete') }"
          aria-hidden="true"
        >
          <span />
        </div>
      </template>
    </div>

    <div v-else-if="!collapsed" class="task-graph-rows">
      <div
        v-for="{ task, index, depth } in displayTasks"
        :key="task.id"
        class="task-graph-row"
        :class="[`task-state-${task.state}`, { 'has-dependencies': depth > 0 }]"
        :style="{ '--task-depth': Math.min(depth, 3) }"
      >
        <span v-if="depth > 0" class="task-tree-connector" aria-hidden="true" />
        <span class="task-state-icon" aria-hidden="true">{{ taskIcon(task, index) }}</span>
        <span class="task-graph-copy">
          <button type="button" class="task-graph-title" @click="toggleBrief(task.id)">
            <strong>{{ task.title }}</strong>
          </button>
          <TaskGraphTaskDetail :task="task" :fallback="taskDetail(task)" />
          <TaskGraphTaskBrief v-if="briefs.has(task.id)" :task="task" />
          <span v-if="task.state === 'running'" class="task-progress" aria-hidden="true">
            <span />
          </span>
        </span>
        <button
          v-if="task.slug && task.child_conversation_id"
          type="button"
          class="task-graph-action"
          @click.stop="openTask(task.slug)"
        >
          <TaskGraphRunningLabel v-if="task.state === 'running'" />
          <template v-else>{{ stateLabel(task.state) }}</template>
          <TaskGraphElapsedTime
            v-if="task.state === 'running' || task.state === 'starting'"
            :started-at="task.started_at"
          />
        </button>
        <span v-else class="task-graph-state-label">
          {{ stateLabel(task.state) }}
          <TaskGraphElapsedTime
            v-if="task.state === 'running' || task.state === 'starting'"
            :started-at="task.started_at"
          />
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { TaskGraphSnapshot, TaskGraphTask, TaskState } from "../../taskGraph";
import { navigateToConversationSlug } from "../composables/subagentLive";
import TaskGraphElapsedTime from "./TaskGraphElapsedTime.vue";
import TaskGraphRunningLabel from "./TaskGraphRunningLabel.vue";
import TaskGraphTaskBrief from "./TaskGraphTaskBrief.vue";
import TaskGraphTaskDetail from "./TaskGraphTaskDetail.vue";

const props = withDefaults(
  defineProps<{
    graph: TaskGraphSnapshot;
    collapsed?: boolean;
    variant?: "dock" | "inline";
    showHeader?: boolean;
  }>(),
  {
    collapsed: false,
    variant: "dock",
    showHeader: true,
  },
);

const emit = defineEmits<{ toggle: [] }>();

const briefs = ref(new Set<string>());

function toggleBrief(id: string) {
  if (!briefs.value.delete(id)) briefs.value.add(id);
}

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
    remaining: tasks.filter((task) => task.state === "pending" || task.state === "ready").length,
  };
});

const displayTasks = computed(() => {
  const depths = new Map<string, number>();
  const taskByID = new Map(props.graph.tasks.map((task) => [task.id, task]));

  function depthFor(task: TaskGraphTask, visiting = new Set<string>()): number {
    const existing = depths.get(task.id);
    if (existing !== undefined) return existing;
    if (visiting.has(task.id)) return 0;
    visiting.add(task.id);
    const depth = (task.depends_on || []).reduce((maxDepth, dependencyID) => {
      const dependency = taskByID.get(dependencyID);
      return dependency ? Math.max(maxDepth, depthFor(dependency, visiting) + 1) : maxDepth;
    }, 0);
    visiting.delete(task.id);
    depths.set(task.id, depth);
    return depth;
  }

  return props.graph.tasks.map((task, index) => ({ task, index, depth: depthFor(task) }));
});

const topology = computed(() => {
  const byDepth = new Map<number, (typeof displayTasks.value)[number][]>();
  for (const item of displayTasks.value) {
    const layer = byDepth.get(item.depth) || [];
    layer.push(item);
    byDepth.set(item.depth, layer);
  }
  const layers = [...byDepth.entries()]
    .sort(([left], [right]) => left - right)
    .map(([depth, tasks]) => ({ depth, tasks }));

  const layered = layers.every((layer, index) => {
    const expected = new Set(index === 0 ? [] : layers[index - 1].tasks.map(({ task }) => task.id));
    return layer.tasks.every(({ task }) => {
      const dependencies = task.depends_on || [];
      if (index === 0) return dependencies.length === 0;
      return (
        dependencies.length > 0 && dependencies.every((dependency) => expected.has(dependency))
      );
    });
  });

  return { layered, layers };
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

const dockSummary = computed(() => {
  const parts = [];
  if (counts.value.running) {
    parts.push(`${counts.value.running} subagent${counts.value.running === 1 ? "" : "s"} running`);
  }
  if (counts.value.remaining) parts.push(`${counts.value.remaining} remaining`);
  if (counts.value.failed) parts.push(`${counts.value.failed} failed`);
  return parts.join(" · ") || summary.value;
});

function stateLabel(state: TaskState): string {
  switch (state) {
    case "complete":
      return "Complete";
    case "failed":
      return "Failed";
    case "cancelled":
      return "Cancelled";
    case "running":
      return "Running";
    case "starting":
      return "Starting";
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
  const dependencies = (task.depends_on || [])
    .map((id) => props.graph.tasks.find((candidate) => candidate.id === id)?.title || id)
    .join(", ");
  if (task.state === "running" || task.state === "starting") {
    return [task.model, dependencies ? `after ${dependencies}` : ""].filter(Boolean).join(" · ");
  }
  if (dependencies) {
    return `${task.state === "pending" ? "Waiting for" : "After"} ${dependencies}`;
  }
  return task.model || "Subagent task";
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

.task-graph-dock + .task-graph-dock {
  border-top: 0;
}

.task-graph-dock {
  flex: none;
  margin: 0;
  border-right: 0;
  border-left: 0;
  border-radius: 0;
  background: var(--bg-primary);
  box-shadow: none;
}

.task-graph-dock.task-graph-running,
.task-graph-dock.task-graph-complete,
.task-graph-dock.task-graph-failed {
  border-color: var(--border);
}

.task-graph-dock .task-graph-header {
  gap: 0.5rem;
  min-height: 2.25rem;
  padding: 0.35rem 1rem;
}

.task-graph-dock .task-graph-indicator {
  width: 1rem;
  height: 1rem;
  font-size: 0.6rem;
}

.task-graph-dock .task-graph-rows {
  max-height: 14rem;
  padding-top: 0.2rem;
  padding-bottom: 0.2rem;
}

.task-graph-dock .task-graph-flow .task-graph-row {
  min-height: 2.25rem;
  padding-top: 0.25rem;
  padding-bottom: 0.25rem;
}

.task-graph-dock .task-graph-heading strong,
.task-graph-dock .task-graph-copy strong {
  font-size: 0.75rem;
}

.task-graph-dock .task-graph-heading small,
.task-graph-dock .task-graph-copy small,
.task-graph-dock .task-graph-state-label,
.task-graph-dock .task-graph-action {
  font-size: 0.625rem;
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
}

.task-graph-copy small {
  display: -webkit-box;
  line-height: 1.35;
  white-space: normal;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.task-graph-chevron {
  width: 1rem;
  height: 1rem;
  flex: none;
  color: var(--text-secondary);
  transition: transform 0.15s ease;
}

.task-graph-chevron.expanded {
  transform: rotate(90deg);
}

.task-graph-rows {
  max-height: 20rem;
  overflow-y: auto;
  border-top: 1px solid var(--border);
}

.task-graph-rationale {
  margin: 0;
  padding: 0.375rem 0.625rem;
  border-top: 1px solid var(--border);
  color: var(--text-secondary);
  font-size: 0.625rem;
  line-height: 1.4;
}

.task-graph-title {
  display: block;
  min-width: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.task-graph-title strong {
  display: block;
}

.task-graph-row {
  --task-depth: 0;
  display: grid;
  grid-template-columns: 1.35rem minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.625rem;
  min-height: 3.5rem;
  padding: 0.625rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.task-graph-row.has-dependencies {
  grid-template-columns: 0.625rem 1.35rem minmax(0, 1fr) auto;
  margin-left: calc(min(var(--task-depth), 3) * 0.75rem);
}

.task-tree-connector {
  position: relative;
  align-self: stretch;
  width: 0.625rem;
}

.task-tree-connector::before {
  position: absolute;
  top: -0.625rem;
  bottom: 50%;
  left: 0.1875rem;
  width: 1px;
  background: var(--border);
  content: "";
}

.task-tree-connector::after {
  position: absolute;
  top: 50%;
  left: 0.1875rem;
  width: 0.4375rem;
  height: 1px;
  background: var(--border);
  content: "";
}

.task-graph-row:last-child {
  border-bottom: 0;
}

.task-graph-flow {
  padding: 0.375rem 0;
}

.task-flow-layer {
  position: relative;
}

.task-flow-junction::before {
  position: absolute;
  left: 1rem;
  width: 1px;
  background: color-mix(in srgb, var(--text-secondary) 24%, var(--border));
  content: "";
}

.task-graph-flow .task-graph-row {
  position: relative;
  grid-template-columns: minmax(0, 1fr) auto;
  min-height: 2.625rem;
  padding: 0.375rem 0.625rem 0.375rem 2.5rem;
  border-bottom: 0;
}

.task-graph-flow .task-graph-row::before {
  position: absolute;
  top: 50%;
  left: 1rem;
  width: 0.5625rem;
  height: 1px;
  background: color-mix(in srgb, var(--text-secondary) 24%, var(--border));
  content: "";
}

.task-graph-flow .task-flow-layer .task-graph-row::after {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 1rem;
  width: 1px;
  background: color-mix(in srgb, var(--text-secondary) 24%, var(--border));
  content: "";
}

.task-graph-flow .task-flow-layer:first-child .task-graph-row:first-child::after {
  top: 50%;
}

.task-graph-flow .task-flow-layer:last-child .task-graph-row:last-child::after {
  bottom: 50%;
}

.task-graph-flow .task-state-icon {
  position: absolute;
  top: 50%;
  left: 1.5625rem;
  z-index: 1;
  width: 1.0625rem;
  height: 1.0625rem;
  font-size: 0.5625rem;
  transform: translateY(-50%);
}

.task-graph-flow .task-state-pending .task-state-icon,
.task-graph-flow .task-state-ready .task-state-icon {
  display: none;
}

.task-flow-junction {
  position: relative;
  height: 0.875rem;
}

.task-flow-junction::before {
  top: 0;
  bottom: 0;
}

.task-flow-junction span {
  position: absolute;
  top: 50%;
  left: 1rem;
  width: 0.375rem;
  height: 0.375rem;
  border: 1px solid color-mix(in srgb, var(--text-secondary) 48%, var(--border));
  border-radius: 1px;
  background: var(--bg-secondary);
  transform: translate(-50%, -50%) rotate(45deg);
}

.task-flow-junction.is-finished span {
  border-color: color-mix(in srgb, var(--accent-primary) 72%, var(--border));
  background: color-mix(in srgb, var(--accent-primary) 72%, var(--border));
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

.task-graph-action time,
.task-graph-state-label time {
  display: block;
  margin-top: 0.0625rem;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
  text-align: right;
}

.task-state-running .task-graph-action,
.task-state-starting .task-graph-action {
  border-color: transparent;
  background: color-mix(in srgb, var(--accent-primary) 8%, transparent);
  color: var(--accent-primary);
}

.task-state-complete .task-graph-action {
  border-color: transparent;
  background: color-mix(in srgb, var(--success-bg) 65%, transparent);
  color: var(--success-text);
}

.task-state-failed .task-graph-action {
  border-color: transparent;
  background: color-mix(in srgb, var(--error-bg) 65%, transparent);
  color: var(--error-text);
}

.task-graph-state-label {
  color: var(--text-tertiary);
  font-size: 0.6875rem;
}

.task-graph-inline {
  margin: 0.5rem 0;
}

.task-graph-inline.collapsed .task-graph-header {
  min-height: 2.625rem;
  padding-top: 0.375rem;
  padding-bottom: 0.375rem;
}

.task-graph-inline.collapsed .task-graph-indicator {
  width: 1.125rem;
  height: 1.125rem;
  font-size: 0.625rem;
}

.task-graph-inline.collapsed .task-graph-heading {
  flex-direction: row;
  align-items: baseline;
  gap: 0.5rem;
}

.task-graph-inline.collapsed .task-graph-heading strong {
  font-size: 0.75rem;
}

.task-graph-inline.collapsed .task-graph-heading small {
  flex: none;
  font-size: 0.625rem;
}

.task-graph.collapsed {
  border-color: transparent;
  border-radius: 0;
  background: transparent;
  box-shadow: none;
}

.task-graph-inline.collapsed {
  margin: 0.125rem 0;
}

.task-graph.collapsed .task-graph-header {
  min-height: 1.625rem;
  padding: 0.125rem 0.375rem;
  gap: 0.375rem;
}

.task-graph.collapsed .task-graph-indicator {
  width: 0.5rem;
  height: 0.5rem;
  color: transparent;
  font-size: 0;
  box-shadow: none;
}

.task-graph.collapsed .task-graph-heading {
  display: block;
}

.task-graph.collapsed .task-graph-heading strong {
  display: block;
  color: var(--text-secondary);
  font-size: 0.6875rem;
  font-weight: 500;
}

.task-graph.collapsed .task-graph-heading small {
  display: none;
}

.task-graph-dock.collapsed .task-graph-heading small {
  display: block;
  font-size: 0.6875rem;
}

.task-graph.collapsed .task-graph-chevron {
  width: 0.75rem;
  height: 0.75rem;
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
    margin: 0;
  }

  .task-graph-header,
  .task-graph-row {
    padding-right: 0.625rem;
    padding-left: 0.625rem;
  }

  .task-graph-row.has-dependencies {
    margin-left: calc(min(var(--task-depth), 2) * 0.4rem);
  }

  .task-graph-dock .task-graph-header {
    padding-right: 0.75rem;
    padding-left: 0.75rem;
  }

  .task-graph-copy strong {
    white-space: normal;
  }

  .task-graph-inline.collapsed .task-graph-heading small {
    overflow: hidden;
    flex: 1;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

@media (prefers-reduced-motion: reduce) {
  .task-progress span {
    animation: none;
  }
}
</style>
