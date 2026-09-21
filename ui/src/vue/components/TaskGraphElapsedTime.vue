<template>
  <time v-if="elapsed">{{ elapsed }}</time>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from "vue";

const props = defineProps<{
  startedAt?: string;
}>();

const now = ref(Date.now());
const timer = window.setInterval(() => {
  now.value = Date.now();
}, 1000);

const elapsed = computed(() => {
  if (!props.startedAt) return "";
  const started = Date.parse(props.startedAt);
  if (!Number.isFinite(started)) return "";
  const seconds = Math.max(0, Math.floor((now.value - started) / 1000));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  if (hours) return `${hours}h ${minutes}m`;
  return `${minutes}m ${remainder.toString().padStart(2, "0")}s`;
});

onUnmounted(() => window.clearInterval(timer));
</script>
