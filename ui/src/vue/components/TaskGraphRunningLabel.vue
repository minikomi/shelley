<template>
  <span class="running-label">
    <span class="sr-only">Running</span>
    <span
      v-for="(char, index) in chars"
      :key="index"
      aria-hidden="true"
      :class="{ 'shimmer-letter': index === shimmerIndex }"
      >{{ char }}</span
    >
  </span>
</template>

<script setup lang="ts">
import { onUnmounted, ref } from "vue";

const chars = "Running".split("");
const shimmerIndex = ref(-1);
let timer: number | null = null;

function advance() {
  if (shimmerIndex.value >= chars.length - 1) {
    shimmerIndex.value = -1;
    timer = window.setTimeout(advance, 1400);
    return;
  }
  shimmerIndex.value++;
  timer = window.setTimeout(advance, 180);
}

timer = window.setTimeout(advance, 700);

onUnmounted(() => {
  if (timer !== null) window.clearTimeout(timer);
});
</script>

<style scoped>
.running-label {
  color: var(--text-primary);
}

.shimmer-letter {
  color: var(--text-tertiary);
}
</style>
