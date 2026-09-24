<!-- Sub-component of Message.vue (ported from GitInfoMessage in
     components/Message.tsx). Compact git-state notification. Preserves the
     .message.message-gitinfo container, data-testid "message-gitinfo", and the
     msg-* classes for the worktree/branch/hash/copy/subject/diff link. -->
<template>
  <div
    v-if="commitHash"
    ref="containerRef"
    class="message message-gitinfo msg-gitinfo-container"
    :data-testid="isTourRequest ? 'message-tour-request' : 'message-gitinfo'"
    @mouseenter="refreshTour"
  >
    <span v-if="isTourRequest">
      Tour for <code class="msg-commit-hash">{{ commitHash.slice(0, 8) }}</code>
      <template v-if="tourState === 'building'">
        {{ " · " }}
        <a
          v-if="tourStatus?.worker_slug"
          :href="`/c/${tourStatus.worker_slug}`"
          class="msg-tour-building"
          @click="onWorkerLinkClick"
        >
          <span class="working-indicator" aria-hidden="true" /> Building ↗
        </a>
        <span v-else class="msg-tour-building">
          <span class="working-indicator" aria-hidden="true" /> Building
        </span>
      </template>
      <template v-else-if="tourState === 'present'">
        {{ " is ready · " }}
        <a v-if="canShowDiff" :href="diffHref" class="msg-diff-link" @click="onDiffLinkClick"
          >Open tour →</a
        >
      </template>
      <template v-else-if="tourState === 'failed' || tourState === 'absent'">
        {{ " could not be built · " }}
        <button
          v-if="canRequestTour"
          type="button"
          class="msg-tour-action"
          :disabled="requestingTour"
          v-tooltip.top="tourStatus?.error || 'The subagent did not attach a valid tour'"
          @click="requestTour"
          >{{ requestingTour ? "Building tour" : "Retry" }}</button
        >
      </template>
      <template v-else>{{ " · Checking tour" }}</template>
    </span>
    <span v-else>
      <span v-if="worktree" class="msg-worktree">{{ worktree }}</span>
      <span v-if="branch" class="msg-branch">{{ branch }}</span>
      {{ branch ? " now at " : "now at " }}
      <code
        class="msg-commit-hash"
        v-tooltip.top="'Click to copy commit hash'"
        @click="handleCopyHash"
        >{{ commitHash }}</code
      >
      <button
        :class="copied ? 'msg-copy-button copied' : 'msg-copy-button'"
        v-tooltip.top="'Copy commit hash'"
        aria-label="Copy commit hash"
        @click="handleCopyHash"
      >
        <svg
          v-if="copied"
          width="12"
          height="12"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          class="msg-icon-middle"
        >
          <polyline points="20 6 9 17 4 12" />
        </svg>
        <svg
          v-else
          width="12"
          height="12"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          class="msg-icon-middle"
        >
          <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
        </svg>
      </button>
      <span v-if="truncatedSubject" class="msg-subject" :title="subject || undefined"
        >"{{ truncatedSubject }}"</span
      >
      <template v-if="canShowDiff">
        {{ " " }}
        <a :href="diffHref" class="msg-diff-link" @click="onDiffLinkClick">diff</a>
      </template>
      <template v-if="tourState === 'present' && canShowDiff">
        {{ " " }}
        <a
          :href="diffHref"
          class="msg-diff-link"
          data-testid="gitinfo-tour-link"
          v-tooltip.top="'Open guided commit tour'"
          @click="onDiffLinkClick"
          >tour</a
        >
      </template>
      <template v-else-if="tourState === 'absent' && canRequestTour">
        {{ " " }}
        <button
          type="button"
          class="msg-tour-action"
          data-testid="gitinfo-tour-request"
          :disabled="requestingTour"
          v-tooltip.top="'Build a guided tour in a subagent'"
          @click="requestTour"
        >
          {{ requestingTour ? "requesting tour" : "request tour" }}
        </button>
      </template>
      <template v-else-if="tourState === 'building'">
        {{ " " }}
        <a
          v-if="tourStatus?.worker_slug"
          :href="`/c/${tourStatus.worker_slug}`"
          class="msg-tour-building"
          data-testid="gitinfo-tour-building"
          v-tooltip.top="'Open the subagent building this tour'"
          @click="onWorkerLinkClick"
        >
          <span class="working-indicator" aria-hidden="true" /> building tour
        </a>
        <span v-else class="msg-tour-building" data-testid="gitinfo-tour-building">
          <span class="working-indicator" aria-hidden="true" /> building tour
        </span>
      </template>
      <template v-else-if="tourState === 'failed' && canRequestTour">
        {{ " " }}
        <span class="msg-tour-failed" data-testid="gitinfo-tour-failed">tour failed</span>
        {{ " " }}
        <button
          type="button"
          class="msg-tour-action"
          :disabled="requestingTour"
          v-tooltip.top="tourStatus?.error || 'The subagent did not attach a valid tour'"
          @click="requestTour"
        >
          retry
        </button>
      </template>
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import type { GitTourBuildStatus } from "../../services/api";
import {
  loadCommitTourStatus,
  requestCommitTour,
  subscribeCommitTourStatus,
} from "../../services/commitTourStatus";
import type { Message as MessageType } from "../../types";
import { navigateToConversationSlug } from "../composables/subagentLive";

const props = defineProps<{
  message: MessageType;
  onOpenDiffViewer?: (commit: string, cwd?: string) => void;
  canRequestTour?: boolean;
}>();

const copied = ref(false);
const containerRef = ref<HTMLElement | null>(null);
const tourStatus = ref<GitTourBuildStatus | null>(null);
const requestingTour = ref(false);
let lastForcedTourProbeAt = 0;
let tourPollTimer: ReturnType<typeof setTimeout> | null = null;
let unsubscribeTour: (() => void) | null = null;
let disposed = false;

const parsed = computed(() => {
  let commitHash: string | null = null;
  let subject: string | null = null;
  let branch: string | null = null;
  let worktree: string | null = null;
  let tourRequest = false;

  if (props.message.user_data) {
    try {
      const userData =
        typeof props.message.user_data === "string"
          ? JSON.parse(props.message.user_data)
          : props.message.user_data;
      if (userData.commit) commitHash = userData.commit;
      if (userData.subject) subject = userData.subject;
      if (userData.branch) branch = userData.branch;
      if (userData.worktree) worktree = userData.worktree;
      tourRequest = userData.tour_request === true;
    } catch (err) {
      console.error("Failed to parse gitinfo user_data:", err);
    }
  }
  return { commitHash, subject, branch, worktree, tourRequest };
});

const isTourRequest = computed(() => parsed.value.tourRequest);
const commitHash = computed(() => parsed.value.commitHash);
const subject = computed(() => parsed.value.subject);
const branch = computed(() => parsed.value.branch);
const worktree = computed(() => parsed.value.worktree);
const tourState = computed(() => tourStatus.value?.status ?? "unknown");
const canShowDiff = computed(() => !!commitHash.value && !!props.onOpenDiffViewer);

const truncatedSubject = computed(() => {
  const s = subject.value;
  return s && s.length > 40 ? s.slice(0, 37) + "..." : s;
});

const diffHref = computed(() => {
  const params = new URLSearchParams();
  params.set("diff", commitHash.value!);
  if (worktree.value) params.set("cwd", worktree.value);
  return `${window.location.pathname}?${params.toString()}`;
});

function handleDiffClick() {
  if (commitHash.value && props.onOpenDiffViewer) {
    props.onOpenDiffViewer(commitHash.value, worktree.value || undefined);
  }
}

function onDiffLinkClick(e: MouseEvent) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  handleDiffClick();
}

function onWorkerLinkClick(e: MouseEvent) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
  const slug = tourStatus.value?.worker_slug;
  if (!slug) return;
  e.preventDefault();
  navigateToConversationSlug(slug);
}

function scheduleTourPoll() {
  if (tourPollTimer) clearTimeout(tourPollTimer);
  tourPollTimer = null;
  if (disposed || tourState.value !== "building") return;
  tourPollTimer = setTimeout(() => void probeTour(true), 2_000);
}

function applyTourStatus(status: GitTourBuildStatus) {
  tourStatus.value = status;
  scheduleTourPoll();
}

async function probeTour(force = false) {
  const hash = commitHash.value;
  const cwd = worktree.value;
  if (!hash || !cwd) return;
  try {
    const status = await loadCommitTourStatus(cwd, hash, force);
    if (!disposed) applyTourStatus(status);
  } catch (error) {
    if (disposed) return;
    console.error("Failed to check commit tour:", error);
    if (isTourRequest.value && tourState.value !== "building") {
      if (tourPollTimer) clearTimeout(tourPollTimer);
      tourPollTimer = setTimeout(() => void probeTour(true), 2_000);
    } else {
      scheduleTourPoll();
    }
  }
}

function refreshTour() {
  const now = Date.now();
  const force =
    (tourState.value === "absent" || tourState.value === "failed") &&
    now - lastForcedTourProbeAt >= 5_000;
  if (force) lastForcedTourProbeAt = now;
  void probeTour(force);
}

async function requestTour() {
  const hash = commitHash.value;
  const cwd = worktree.value;
  if (!hash || !cwd || !props.message.conversation_id || requestingTour.value) return;
  requestingTour.value = true;
  try {
    applyTourStatus(await requestCommitTour(props.message.conversation_id, cwd, hash));
  } catch (error) {
    applyTourStatus({
      status: "failed",
      hash,
      error: error instanceof Error ? error.message : String(error),
    });
  } finally {
    requestingTour.value = false;
  }
}

let tourObserver: IntersectionObserver | null = null;
onMounted(() => {
  const hash = commitHash.value;
  const cwd = worktree.value;
  if (hash && cwd) unsubscribeTour = subscribeCommitTourStatus(cwd, hash, applyTourStatus);
  if (!containerRef.value || !("IntersectionObserver" in window)) {
    void probeTour();
    return;
  }
  tourObserver = new IntersectionObserver((entries) => {
    if (!entries.some((entry) => entry.isIntersecting)) return;
    tourObserver?.disconnect();
    tourObserver = null;
    void probeTour();
  });
  tourObserver.observe(containerRef.value);
});
onBeforeUnmount(() => {
  disposed = true;
  tourObserver?.disconnect();
  unsubscribeTour?.();
  if (tourPollTimer) clearTimeout(tourPollTimer);
});

function handleCopyHash(e: MouseEvent) {
  e.preventDefault();
  if (commitHash.value) {
    navigator.clipboard.writeText(commitHash.value).then(() => {
      copied.value = true;
      setTimeout(() => (copied.value = false), 1500);
    });
  }
}
</script>
