<template>
  <p v-if="hasError" class="text-secondary" role="alert">Chapter not saved: {{ resultText(toolResult) }}</p>
</template>

<script setup lang="ts">
import { watch } from "vue";
import { inputFields, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();
watch(
  () => toolComplete(props),
  (complete) => {
    if (complete && !props.hasError) {
      window.dispatchEvent(new CustomEvent("shelley-chapters-updated", {
        detail: inputFields<{ conversation_id: string }>(props.display).conversation_id,
      }));
    }
  },
  { immediate: true },
);
</script>
