<template>
  <div class="running-commands" role="group" aria-label="Commands">
    <div v-if="error" class="background-jobs-error" role="alert">{{ error }}</div>
    <div v-if="liveJobs === null" class="background-jobs-empty">{{ error ? "" : "Loading…" }}</div>
    <div v-else-if="jobs.length === 0" class="background-jobs-empty">No running jobs.</div>
    <details
      v-for="job in jobs"
      :key="job.job_id"
      class="running-command"
      data-testid="background-job"
    >
      <summary>
        <span class="running-command-name" :title="job.command">{{ job.command }}</span>
        <span class="running-command-kind">Command</span>
        <RunningToolTime
          v-if="runningIds.has(job.job_id)"
          v-slot="{ elapsed }"
          class="running-command-time"
          :start-time="job.started_at"
        >
          Running · {{ elapsed }}
        </RunningToolTime>
        <span v-else class="running-command-time">Finished</span>
        <span class="running-command-chevron" aria-hidden="true">⌄</span>
      </summary>
      <div class="running-command-body">
        <div class="running-command-output-heading">
          <span>Output</span>
          <button
            v-if="runningIds.has(job.job_id)"
            type="button"
            class="running-command-stop"
            data-testid="background-job-kill"
            :disabled="killing.has(job.job_id)"
            :aria-label="`Stop command: ${job.command}`"
            @click="stop(job.job_id)"
          >
            {{ killing.has(job.job_id) ? "Stopping…" : "Stop command" }}
          </button>
        </div>
        <pre class="running-command-output" data-testid="background-job-tail">{{
          job.tail || "No output yet."
        }}</pre>
      </div>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, inject } from "vue";
import type { BackgroundJob } from "../../services/api";
import { BackgroundJobsKey } from "../composables/backgroundJobs";
import RunningToolTime from "./tools/RunningToolTime.vue";

defineProps<{ jobs: BackgroundJob[] }>();

const backgroundJobs = inject(BackgroundJobsKey);
if (!backgroundJobs) throw new Error("BackgroundJobsList requires BackgroundJobsKey");
const { jobs: liveJobs, error, killing, stop } = backgroundJobs;
const runningIds = computed(() => new Set((liveJobs.value ?? []).map((job) => job.job_id)));
</script>

<style scoped>
.running-command + .running-command {
  border-top: 1px solid var(--border);
}

.running-command > summary {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  list-style: none;
  cursor: pointer;
}

.running-command > summary::-webkit-details-marker {
  display: none;
}

.running-command-name {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
}

.running-command-kind,
.running-command-time,
.running-command-chevron {
  flex-shrink: 0;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}

.running-command[open] .running-command-chevron {
  transform: rotate(180deg);
}

.running-command-body {
  padding: 0 14px 14px;
}

.running-command-output-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}

.running-command-stop {
  padding: 5px 9px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  cursor: pointer;
}

.running-command-stop:hover {
  background: var(--bg-secondary);
}

.running-command-stop:disabled {
  color: var(--text-secondary);
  cursor: default;
}

.running-command-output {
  margin: 0;
  padding: 12px;

  border-radius: 6px;
  background: var(--bg-secondary);
  color: var(--text-secondary);
  font-size: var(--font-size-12);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

@media (max-width: 600px) {
  .running-command-kind {
    display: none;
  }
}
</style>
