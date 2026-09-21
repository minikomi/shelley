<!-- One message chunk of the transcript: either the real content or (below
     the tail-first mount floor) a fixed-height placeholder. See chunkMount.ts
     and the tail-first block in ChatInterface.vue for the design. -->
<template>
  <div v-if="mounted" class="messages-chunk" :class="{ 'messages-chunk--live': chunk.live }">
    <MessageRenderNode
      v-for="node in chunk.nodes"
      :key="node.key"
      v-memo="nodeMemo(node)"
      :node="node"
      :conversation-id="conversationId"
      :on-open-diff-viewer="onOpenDiffViewer"
      :can-request-tour="canRequestTour"
      :on-comment-text-change="onCommentTextChange"
      :on-fork="onFork"
    />
  </div>
  <PendingChunk v-else @reveal="reveal" />
</template>

<script setup lang="ts">
import { computed, inject } from "vue";
import type { RenderChunk, RenderNode } from "./renderNode";
import { chunkMountKey } from "./chunkMount";
import MessageRenderNode from "./MessageRenderNode.vue";
import PendingChunk from "./PendingChunk.vue";

const props = defineProps<{
  chunk: RenderChunk;
  conversationId: string | null;
  onOpenDiffViewer: (commit: string, cwd?: string) => void;
  canRequestTour: boolean;
  onCommentTextChange: (text: string) => void;
  onFork: (messageId: string) => void;
}>();

const mount = inject(chunkMountKey, null);
// Mounted when: above the swept-top watermark (background sweep), at/after
// the tail floor, individually revealed (near-viewport / jump target), or a
// live tail chunk (which always renders — it holds the streaming tail the
// initial floor is sized to include). Mounting is effectively sticky within
// a conversation; all state resets on switch, when this tree is torn down.
const mounted = computed(
  () =>
    !mount ||
    props.chunk.live === true ||
    props.chunk.globalIndex >= mount.floor.value ||
    props.chunk.globalIndex < mount.sweptTop.value ||
    mount.revealed.has(props.chunk.key),
);

function reveal() {
  mount?.reveal(props.chunk.globalIndex);
}

function nodeMemo(node: RenderNode): unknown[] {
  if (node.kind === "message") return [node.item.message];
  if (node.kind === "tool-call") {
    return [
      node.item.toolResult,
      node.item.toolError,
      node.item.hasResult,
      node.item.display,
      node.item.toolStartTime,
      node.item.toolEndTime,
    ];
  }
  if (node.kind === "btw") return [node.exchanges];
  if (node.kind === "carried-band") return [node.count, node.children];
  if (node.kind === "timestamp") return [node.createdAt];
  if (node.kind === "token-marker") return [node.label, node.ctx];
  return [node.label];
}
</script>
