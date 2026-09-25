<template>
  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
    <path
      stroke-linecap="round"
      stroke-linejoin="round"
      :stroke-width="2"
      :d="paths[kind]"
    />
  </svg>
</template>

<script setup lang="ts">
defineProps<{ kind: "duplicate" | "edit" | "delete" }>();

const paths = {
  duplicate:
    "M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z",
  edit: "M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z",
  delete:
    "M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16",
} as const;
</script>
