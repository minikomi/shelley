<!-- In-place compaction audit. Original conversation messages remain unchanged. -->
<template>
  <details
    class="msg-compaction-event"
    data-testid="message-inplacecompaction"
    :data-message-id="message.message_id"
  >
    <summary class="message message-gitinfo msg-distill-container msg-compaction-event-header">
      <span class="msg-compaction-event-label">
        Compacted context<span v-if="counts"> · {{ counts }}</span>
      </span>
      <ToolChevron :expanded="false" />
    </summary>
    <div class="msg-compaction-event-body">
      <table
        v-if="data.squishes?.length"
        class="msg-compaction-table"
        aria-label="Compacted messages"
      >
        <thead>
          <tr>
            <th scope="col">Messages</th>
            <th scope="col">Summary</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in data.squishes" :key="s.from_sequence_id">
            <td>
              <code>#{{ s.from_sequence_id }}–#{{ s.to_sequence_id }}</code>
            </td>
            <td>
              <details class="msg-compaction-summary">
                <summary
                  :aria-label="`Summary of messages #${s.from_sequence_id}–#${s.to_sequence_id}`"
                >
                  <span class="msg-compaction-preview">{{ s.summary }}</span>
                  <ToolChevron :expanded="false" />
                </summary>
                <div class="msg-compaction-summary-text">{{ s.summary }}</div>
              </details>
            </td>
          </tr>
        </tbody>
      </table>
      <details v-if="data.trims?.length" class="msg-compaction-footer">
        <summary>
          {{ plural(data.trims.length, "tool output") }} trimmed
          <ToolChevron :expanded="false" />
        </summary>
        <div class="msg-compaction-ids">
          {{ data.trims.map((t) => `#${t.sequence_id}`).join(", ") }}
        </div>
      </details>
      <details v-if="hiddenCount" class="msg-compaction-footer">
        <summary>
          {{ plural(hiddenCount, "housekeeping record") }} hidden
          <ToolChevron :expanded="false" />
        </summary>
        <p>Hid {{ hidden }} from the agent. Original conversation messages remain unchanged.</p>
      </details>
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { InPlaceCompaction } from "../../generated-types";
import type { Message as MessageType } from "../../types";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{ message: MessageType }>();

const data = computed<InPlaceCompaction>(() => {
  const raw = props.message.user_data;
  if (!raw) return {};
  return typeof raw === "string" ? JSON.parse(raw) : (raw as InPlaceCompaction);
});

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

const counts = computed(() => {
  const parts: string[] = [];
  if (data.value.squishes?.length) parts.push(plural(data.value.squishes.length, "group"));
  if (data.value.trims?.length) parts.push(plural(data.value.trims.length, "tool output"));
  return parts.join(", ");
});

const hiddenCount = computed(
  () =>
    (data.value.hidden_sequence_ids?.length ?? 0) + (data.value.hidden_tool_use_ids?.length ?? 0),
);

const hidden = computed(() => {
  const parts: string[] = [];
  const nudges = data.value.hidden_sequence_ids?.length ?? 0;
  const calls = data.value.hidden_tool_use_ids?.length ?? 0;
  if (nudges) parts.push(plural(nudges, "context nudge"));
  if (calls) parts.push(plural(calls, "compaction call"));
  return parts.join(" and ");
});
</script>
