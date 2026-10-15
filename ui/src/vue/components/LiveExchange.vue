<template>
  <article class="live-exchange" aria-label="Live exchange" data-testid="live-exchange">
    <header>
      <span class="live-exchange-dot" aria-hidden="true" />
      <strong>Live exchange</strong>
      <span class="live-exchange-note">Not sent to Shelley</span>
    </header>
    <div v-for="(turn, index) in turns" :key="index" class="live-exchange-turn">
      <span class="live-exchange-speaker">{{ turn.user ? "You" : "Live" }}</span>
      <p>{{ turn.text }}</p>
    </div>
  </article>
</template>

<script setup lang="ts">
import type { LiveTranscriptTurn } from "../../services/liveTranscript";

defineProps<{ turns: LiveTranscriptTurn[] }>();
</script>

<style scoped>
.live-exchange {
  border-inline-start: 2px solid var(--green-600);
  padding: 0.5rem 0 0.5rem 0.875rem;
  color: var(--text-secondary);
}
.live-exchange header {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
  font-size: 0.75rem;
}
.live-exchange header strong {
  color: var(--text-primary);
  font-weight: 600;
}
.live-exchange-dot {
  width: 0.375rem;
  height: 0.375rem;
  border-radius: 50%;
  background: var(--green-600);
}
.live-exchange-note {
  margin-inline-start: auto;
  font-size: 0.6875rem;
}
.live-exchange-turn {
  display: grid;
  grid-template-columns: 2rem minmax(0, 1fr);
  gap: 0.75rem;
  margin-top: 0.5rem;
}
.live-exchange-speaker {
  padding-top: 0.125rem;
  font-size: 0.6875rem;
}
.live-exchange-turn p {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
