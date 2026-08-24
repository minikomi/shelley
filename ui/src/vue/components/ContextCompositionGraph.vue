<!-- Reconstructed context composition per LLM call. The top line is the
     provider-reported context size; colored stacked areas estimate which
     visible history categories made it up. -->
<template>
  <div class="context-composition-graph">
    <template v-if="points.length > 0 && props.maxContextTokens > 0">
      <div class="context-composition-graph-header">
        <span>estimated composition</span>
        <span>{{ formatTokenCount(points.at(-1)!.total) }} / {{ formatTokenCount(props.maxContextTokens) }} window</span>
      </div>
      <svg
        :viewBox="`0 0 ${W} ${H}`"
        class="context-composition-graph-svg"
        role="img"
        :aria-label="`Context composition across ${points.length} LLM calls`"
        @mousemove="onMove"
        @mouseleave="clearHover"
      >
        <line
          v-for="threshold in visibleThresholds"
          :key="threshold.label"
          :x1="PADL"
          :y1="yAtTokens(threshold.tokens)"
          :x2="W - PADR"
          :y2="yAtTokens(threshold.tokens)"
          :class="['context-composition-threshold', { 'context-composition-threshold-compact': threshold.fraction >= 0.7 }]"
        />
        <path v-for="(category, index) in CATEGORIES" :key="category.key" :d="areaPath(index)" :class="`context-composition-area-${category.key}`" />
        <polyline :points="totalLine" class="context-composition-total-line" />
        <line :x1="PADL" :y1="H - PADB" :x2="W - PADR" :y2="H - PADB" class="context-composition-axis" />
        <line
          v-if="hoverX !== null"
          :x1="hoverX"
          :y1="PADT"
          :x2="hoverX"
          :y2="H - PADB"
          class="context-composition-hover-line"
        />
        <text
          v-for="tick in yTicks"
          :key="tick"
          :x="PADL - 4"
          :y="yAtTokens(tick) + 3"
          text-anchor="end"
          class="context-composition-label"
        >{{ formatTokenCount(tick) }}</text>
        <text :x="PADL" :y="H - 4" class="context-composition-label">1</text>
        <text :x="W - PADR" :y="H - 4" text-anchor="end" class="context-composition-label">{{ points.length }}</text>
      </svg>
      <div class="context-composition-legend">
        <span v-for="category in CATEGORIES" :key="category.key">
          <i :class="`context-composition-chip-${category.key}`" />{{ category.label }}
        </span>
      </div>
      <div class="context-composition-readout">
        <template v-if="hoverPoint">
          call {{ hoverIndex! + 1 }} of {{ points.length }} ·
          <b>{{ formatTokenCount(hoverPoint.total) }}</b> · {{ (hoverPoint.total / props.maxContextTokens * 100).toFixed(1) }}%
        </template>
        <template v-else>
          current <b>{{ formatTokenCount(points.at(-1)!.total) }}</b> · {{ (points.at(-1)!.total / props.maxContextTokens * 100).toFixed(1) }}%
        </template>
      </div>
    </template>
    <div v-else class="context-composition-readout">No context data yet.</div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { LLMContent, Message, Usage } from "../../types";
import { formatTokenCount } from "../../utils/tokenCostGraph";

const props = defineProps<{
  messages: Message[];
  maxContextTokens: number;
}>();

const TYPE_TEXT = 2;
const TYPE_THINKING = 3;
const TYPE_TOOL_USE = 5;
const TYPE_TOOL_RESULT = 6;
const TYPE_WEB_SEARCH_TOOL_RESULT = 8;
const W = 280;
const H = 150;
const PADL = 32;
const PADR = 6;
const PADT = 6;
const PADB = 18;
const plotWidth = W - PADL - PADR;
const plotHeight = H - PADT - PADB;

const CATEGORIES = [
  { key: "text", label: "text" },
  { key: "toolCalls", label: "tool calls" },
  { key: "toolOutput", label: "tool output" },
  { key: "images", label: "images" },
] as const;
type Category = (typeof CATEGORIES)[number]["key"];
type Composition = Record<Category, number>;
type Point = Composition & { total: number };

const points = computed<Point[]>(() => {
  const running: Composition = { text: 0, toolCalls: 0, toolOutput: 0, images: 0 };
  const out: Point[] = [];
  let generation: number | undefined;

  for (const message of props.messages) {
    if (generation !== undefined && message.generation !== generation) {
      running.text = 0;
      running.toolCalls = 0;
      running.toolOutput = 0;
      running.images = 0;
    }
    generation = message.generation;
    addMessage(running, message);
    if (message.type !== "agent") continue;
    const usage = parseUsage(message);
    const total = usage ? contextWindowUsed(usage) : 0;
    if (total === 0) continue;

    const estimated = CATEGORIES.reduce((sum, category) => sum + running[category.key], 0);
    const scale = estimated > 0 ? total / estimated : 0;
    out.push({
      total,
      text: Math.round(running.text * scale),
      toolCalls: Math.round(running.toolCalls * scale),
      toolOutput: Math.round(running.toolOutput * scale),
      images: Math.round(running.images * scale),
    });
  }
  return out;
});

const chartMax = computed(() => niceTokenCeiling(Math.max(...points.value.map((point) => point.total), 1)));
const yTicks = computed(() => [0, Math.round(chartMax.value / 2), chartMax.value]);
const visibleThresholds = computed(() =>
  [
    { fraction: 0.4, label: "40%" },
    { fraction: 0.55, label: "55%" },
    { fraction: 0.7, label: "70%" },
  ]
    .map((threshold) => ({ ...threshold, tokens: props.maxContextTokens * threshold.fraction }))
    .filter((threshold) => threshold.tokens <= chartMax.value),
);

const hoverIndex = ref<number | null>(null);
const hoverX = ref<number | null>(null);
const hoverPoint = computed(() =>
  hoverIndex.value === null ? null : points.value[hoverIndex.value] || null,
);
const totalLine = computed(() => points.value.map((point, index) => `${xAt(index)},${yAtTokens(point.total)}`).join(" "));

function areaPath(categoryIndex: number) {
  if (points.value.length === 0) return "";
  const upper = points.value.map((point, index) => {
    const sum = CATEGORIES.slice(0, categoryIndex + 1).reduce((total, category) => total + point[category.key], 0);
    return `${xAt(index)},${yAtTokens(sum)}`;
  });
  const lower = points.value
    .map((point, index) => {
      const sum = CATEGORIES.slice(0, categoryIndex).reduce((total, category) => total + point[category.key], 0);
      return `${xAt(index)},${yAtTokens(sum)}`;
    })
    .reverse();
  return `M${upper.join(" L")} L${lower.join(" L")} Z`;
}

function xAt(index: number) {
  if (points.value.length <= 1) return PADL + plotWidth / 2;
  return PADL + (index / (points.value.length - 1)) * plotWidth;
}

function yAtTokens(tokens: number) {
  return PADT + (1 - Math.min(1, Math.max(0, tokens / chartMax.value))) * plotHeight;
}

function onMove(event: MouseEvent) {
  if (points.value.length === 0) return;
  const rect = (event.currentTarget as SVGElement).getBoundingClientRect();
  const pointerX = ((event.clientX - rect.left) / rect.width) * W;
  hoverX.value = Math.min(W - PADR, Math.max(PADL, pointerX));
  let nearest = 0;
  let distance = Infinity;
  for (let index = 0; index < points.value.length; index++) {
    const nextDistance = Math.abs(xAt(index) - hoverX.value);
    if (nextDistance < distance) {
      nearest = index;
      distance = nextDistance;
    }
  }
  hoverIndex.value = nearest;
}

function clearHover() {
  hoverX.value = null;
  hoverIndex.value = null;
}

function addMessage(running: Composition, message: Message) {
  if (!message.llm_data) return;
  try {
    const llm = typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    for (const content of (llm?.Content || []) as LLMContent[]) addContent(running, content, "text");
  } catch {
    // A malformed historic payload stays visible in the conversation but
    // cannot contribute to a reconstructed graph.
  }
}

function addContent(running: Composition, content: LLMContent, fallback: Category) {
  if (content.MediaType || content.DisplayImageURL || content.Data) {
    running.images += Math.max(1, estimateTokens(content.Data || ""));
    return;
  }
  switch (content.Type) {
    case TYPE_TOOL_USE:
      running.toolCalls += estimateTokens(content.ToolName || "") + estimateTokens(stringify(content.ToolInput));
      return;
    case TYPE_TOOL_RESULT:
    case TYPE_WEB_SEARCH_TOOL_RESULT:
      for (const result of content.ToolResult || []) addContent(running, result, "toolOutput");
      return;
    case TYPE_TEXT:
    case TYPE_THINKING:
      running[fallback] += estimateTokens(content.Text || content.Thinking || "");
      return;
    default:
      running[fallback] += estimateTokens(content.Text || "");
  }
}

function parseUsage(message: Message): Usage | null {
  if (!message.usage_data) return null;
  try {
    return typeof message.usage_data === "string" ? JSON.parse(message.usage_data) : message.usage_data;
  } catch {
    return null;
  }
}

function contextWindowUsed(usage: Usage) {
  return (
    (usage.input_tokens || 0) +
    (usage.cache_creation_input_tokens || 0) +
    (usage.cache_read_input_tokens || 0) +
    (usage.output_tokens || 0)
  );
}

function estimateTokens(value: string) {
  return value ? Math.ceil(new TextEncoder().encode(value).length / 4) : 0;
}

function stringify(value: unknown) {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value) || "";
  } catch {
    return "";
  }
}

function niceTokenCeiling(tokens: number) {
  const target = tokens * 1.1;
  const magnitude = 10 ** Math.floor(Math.log10(target));
  for (const multiple of [1, 2, 5, 10]) {
    const ceiling = multiple * magnitude;
    if (ceiling >= target) return ceiling;
  }
  return target;
}
</script>
