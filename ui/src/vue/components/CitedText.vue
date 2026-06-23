<!-- Vue port of components/CitedText.tsx. Renders one coalesced run of
     adjacent text blocks: marker-augmented markdown plus a numbered Sources
     list in markdown mode, or plain inline-formatted text otherwise. The
     coalescing + marker logic lives in utils/coalesceContent.ts, shared with
     React. -->
<template>
  <div v-if="!renderMarkdown" class="whitespace-pre-wrap break-words">
    <InlineText :text="text" />
  </div>
  <template v-else>
    <div class="markdown-content break-words" v-html="html"></div>
    <ol v-if="citations.length > 0" class="citation-sources">
      <li v-for="(c, i) in citations" :key="i" class="citation-source">
        <span class="citation-source-num">{{ c.num }}</span>
        <a
          :href="c.url"
          target="_blank"
          rel="noopener noreferrer"
          class="citation-source-link"
          :title="c.url"
          >{{ c.title || c.url }}</a
        >
      </li>
    </ol>
  </template>
</template>

<script setup lang="ts">
import { computed } from "vue";
import InlineText from "./InlineText.vue";
import { renderMarkdownToSafeHTML } from "../../utils/markdownRender";
import type { Citation } from "../../utils/coalesceContent";

const props = defineProps<{
  text: string;
  markdownText: string;
  citations: Citation[];
  renderMarkdown: boolean;
  messageId?: string;
}>();

const html = computed(() => renderMarkdownToSafeHTML(props.markdownText, props.messageId));
</script>
