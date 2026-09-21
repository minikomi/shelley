<template>
  <div class="task-brief">
    <small v-if="meta">{{ meta }}</small>
    <p v-if="context"><b>Shared context:</b> {{ context }}</p>
    <p>{{ task.prompt?.trim() || "No brief." }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { TaskGraphTask } from "../../taskGraph";

const props = defineProps<{
  task: TaskGraphTask;
  tasks: TaskGraphTask[];
  context?: string;
}>();

const meta = computed(() => {
  const dependencies = (props.task.depends_on || [])
    .map((id) => props.tasks.find((candidate) => candidate.id === id)?.title || id)
    .join(", ");
  const scopes = (props.task.file_scopes || []).join(", ");
  return [
    props.task.reasoning ? `${props.task.reasoning} reasoning` : "",
    dependencies ? `after ${dependencies}` : "",
    scopes ? `scope ${scopes}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
});
</script>

<style scoped>
.task-brief {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  margin-top: 0.25rem;
  color: var(--text-secondary);
  font-size: 0.625rem;
  line-height: 1.4;
}

.task-brief p {
  max-height: 9rem;
  margin: 0;
  overflow-y: auto;
  white-space: pre-wrap;
}
</style>
