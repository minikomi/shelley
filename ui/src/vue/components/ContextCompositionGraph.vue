<!-- Reconstructed context composition per LLM call. The top line is the
     provider-reported context size; colored stacked areas estimate which
     visible history categories made it up. -->
<template>
  <div class="context-composition-graph">
    <template v-if="points.length > 0 && props.maxContextTokens > 0">
      <div class="context-composition-graph-header">
        <span>estimated composition</span>
        <slot name="mode-controls" />
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
        <path
          v-for="(category, index) in categories"
          :key="category.key"
          :d="areaPath(index)"
          :fill="category.color"
          class="context-composition-area"
        />
        <line
          v-for="index in compactionStarts"
          :key="`compaction-${index}`"
          :x1="xAt(index)"
          :y1="PADT"
          :x2="xAt(index)"
          :y2="H - PADB"
          class="context-composition-compaction-line"
        />
        <text
          v-for="index in compactionStarts"
          :key="`compaction-label-${index}`"
          :x="xAt(index) + 4"
          :y="PADT + 10"
          class="context-composition-compaction-label"
        >compacted</text>
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
        <span
          v-for="category in categories"
          :key="category.key"
          v-tooltip.top="category.hint"
          :aria-label="`${category.label}: ${category.hint}`"
        >
          <i :style="{ background: category.color }" />{{ category.label }}
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
      <div v-if="compactionStarts.length" class="context-composition-compaction-note">
        Dashed lines mark compactions.
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
const H = 275;
const PADL = 32;
const PADR = 6;
const PADT = 6;
const PADB = 18;
const plotWidth = W - PADL - PADR;
const plotHeight = H - PADT - PADB;

type Composition = Record<string, number>;
type Point = { total: number; graphTotal: number; generation: number; parts: Composition };
type Category = { key: string; label: string; color: string; hint: string };

const TOOL_CATEGORIES = [
  "bash:code search",
  "bash:file read",
  "bash:build/test",
  "bash:script/query",
  "bash:system",
  "repo/edit",
  "bash:other",
  "tool:browser/web",
  "tool:other",
] as const;

const CATEGORY_LABELS: Record<string, string> = {
  "bash:code search": "bash · code search",
  "bash:file read": "bash · file read",
  "bash:build/test": "bash · build/test",
  "bash:script/query": "bash · script/query",
  "bash:system": "bash · system",
  "repo/edit": "repo/edit",
  "bash:other": "bash · other",
  "tool:browser/web": "browser/web",
  "tool:other": "other tools",
};

const CATEGORY_HINTS: Record<string, string> = {
  "bash:code search": "bash: rg, grep, find, fd",
  "bash:file read": "bash: cat, sed, head, tail, awk, ls, pwd",
  "bash:build/test": "bash: go, pnpm, npm, yarn, make, cargo, pytest, jest, vitest",
  "bash:script/query": "bash: python, python3, node, ruby, perl, sqlite3, psql, mysql",
  "bash:system": "bash: tmux, sudo, curl, wget, df, du, ss",
  "repo/edit": "bash: git, rm, mkdir, gofmt, chmod, mv, cp; tools: apply_patch, patch, write_file",
  "bash:other": "Other bash commands",
  "tool:browser/web": "browser, web_search, keyword_search",
  "tool:other": "All other tools",
};

const CATEGORY_COLORS: Record<string, string> = {
  // Cost graph-adjacent blue, purple, teal, and orange hues, with spaced
  // shades for neighboring context bands.
  "bash:code search": "hsl(199 92% 56%)",
  "bash:file read": "hsl(199 68% 66%)",
  "bash:build/test": "hsl(234 75% 59%)",
  "bash:script/query": "hsl(350 66% 56%)",
  "bash:system": "hsl(27 96% 57%)",
  "repo/edit": "hsl(45 80% 54%)",
  "bash:other": "hsl(0 0% 54%)",
  "tool:browser/web": "hsl(270 58% 56%)",
  "tool:other": "hsl(213 15% 53%)",
};

const points = computed<Point[]>(() => {
  const running: Composition = {};
  const toolKeys = new Map<string, string>();
  const raw: { total: number; generation: number; parts: Composition }[] = [];
  let generation: number | undefined;

  for (const message of props.messages) {
    if (generation !== undefined && message.generation !== generation) {
      for (const key of Object.keys(running)) delete running[key];
      toolKeys.clear();
    }
    generation = message.generation;
    addMessage(running, toolKeys, message);
    if (message.type !== "agent") continue;
    const usage = parseUsage(message);
    const total = usage ? contextWindowUsed(usage) : 0;
    if (total === 0) continue;

    const estimated = Object.values(running).reduce((sum, tokens) => sum + tokens, 0);
    raw.push({
      total,
      generation: message.generation,
      parts: estimated > 0 ? { ...running } : { text: total },
    });
  }

  // A per-call scale made old text appear to shrink whenever a large tool
  // result changed the estimate/provider ratio. Calibrate once at the last
  // call in each generation instead: within a generation, reconstructed
  // context is cumulative and must only grow. A compaction starts a new
  // generation and is the one legitimate reset.
  const scaleByGeneration = new Map<number, number>();
  for (const point of raw) {
    const estimated = Object.values(point.parts).reduce((sum, tokens) => sum + tokens, 0);
    scaleByGeneration.set(point.generation, estimated > 0 ? point.total / estimated : 1);
  }
  return raw.map((point) => {
    const scale = scaleByGeneration.get(point.generation) || 1;
    const parts = Object.fromEntries(
      Object.entries(point.parts).map(([key, tokens]) => [key, Math.round(tokens * scale)]),
    );
    return {
      total: point.total,
      graphTotal: Object.values(parts).reduce((sum, tokens) => sum + tokens, 0),
      generation: point.generation,
      parts,
    };
  });
});

const categories = computed<Category[]>(() => {
  const keys = new Set<string>(["text"]);
  for (const point of points.value) {
    for (const key of Object.keys(point.parts)) keys.add(key);
  }
  return [
    { key: "text", label: "text", color: "hsl(160 64% 48%)", hint: "User and assistant text, including thinking" },
    ...TOOL_CATEGORIES.filter((key) => keys.has(key)).map((key) => ({
      key,
      label: CATEGORY_LABELS[key],
      color: CATEGORY_COLORS[key],
      hint: CATEGORY_HINTS[key],
    })),
    ...(keys.has("images")
      ? [{ key: "images", label: "images", color: "hsl(325 65% 58%)", hint: "Image content in messages or tool results" }]
      : []),
  ];
});

const chartMax = computed(() => niceTokenCeiling(Math.max(...points.value.map((point) => point.graphTotal), 1)));
const yTicks = computed(() => [0, Math.round(chartMax.value / 2), chartMax.value]);
const compactionStarts = computed(() =>
  points.value.flatMap((point, index) =>
    index > 0 && point.generation !== points.value[index - 1].generation ? [index] : [],
  ),
);
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
function areaPath(categoryIndex: number) {
  if (points.value.length === 0) return "";
  const upper = points.value.map((point, index) => {
    const sum = categories.value
      .slice(0, categoryIndex + 1)
      .reduce((total, category) => total + (point.parts[category.key] || 0), 0);
    return `${xAt(index)},${yAtTokens(sum)}`;
  });
  const lower = points.value
    .map((point, index) => {
      const sum = categories.value
        .slice(0, categoryIndex)
        .reduce((total, category) => total + (point.parts[category.key] || 0), 0);
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

function addMessage(running: Composition, toolKeys: Map<string, string>, message: Message) {
  if (!message.llm_data) return;
  try {
    const llm = typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    for (const content of (llm?.Content || []) as LLMContent[]) addContent(running, toolKeys, content, "text");
  } catch {
    // A malformed historic payload stays visible in the conversation but
    // cannot contribute to a reconstructed graph.
  }
}

function addContent(running: Composition, toolKeys: Map<string, string>, content: LLMContent, fallback: string) {
  if (content.MediaType || content.DisplayImageURL || content.Data) {
    addTokens(running, "images", Math.max(1, estimateTokens(content.Data || "")));
    return;
  }
  switch (content.Type) {
    case TYPE_TOOL_USE: {
      const key = toolKey(content.ToolName, content.ToolInput);
      toolKeys.set(content.ID, key);
      addTokens(running, key, estimateTokens(content.ToolName || "") + estimateTokens(stringify(content.ToolInput)));
      return;
    }
    case TYPE_TOOL_RESULT:
    case TYPE_WEB_SEARCH_TOOL_RESULT: {
      const key = toolKeys.get(content.ToolUseID || "") || toolKey(content.Type === TYPE_WEB_SEARCH_TOOL_RESULT ? "web_search" : "other");
      for (const result of content.ToolResult || []) addContent(running, toolKeys, result, key);
      return;
    }
    case TYPE_TEXT:
    case TYPE_THINKING:
      addTokens(running, fallback, estimateTokens(content.Text || content.Thinking || ""));
      return;
    default:
      addTokens(running, fallback, estimateTokens(content.Text || ""));
  }
}

function addTokens(running: Composition, key: string, tokens: number) {
  running[key] = (running[key] || 0) + tokens;
}

function toolKey(name: string | undefined, input?: unknown) {
  if (name !== "bash") {
    switch (name) {
      case "browser":
      case "web_search":
      case "keyword_search":
        return "tool:browser/web";
      case "apply_patch":
      case "patch":
      case "write_file":
        return "repo/edit";
      default:
        return "tool:other";
    }
  }
  const command = commandFromInput(input);
  return `bash:${bashCommandFamily(command)}`;
}

function commandFromInput(input: unknown) {
  if (typeof input === "object" && input && "command" in input && typeof input.command === "string") {
    return input.command;
  }
  if (typeof input !== "string") return "other";
  try {
    const parsed = JSON.parse(input);
    return typeof parsed?.command === "string" ? parsed.command : input;
  } catch {
    return input;
  }
}

function bashCommandFamily(command: string) {
  for (const segment of command.trim().split(/&&|;|\n/)) {
    const words = segment.trim().split(/\s+/).filter(Boolean);
    while (words.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[0])) words.shift();
    if (!words.length || ["cd", "export", "set", "true"].includes(words[0])) continue;
    if (words[0] === "git" || ["rm", "mkdir", "gofmt", "chmod", "mv", "cp"].includes(words[0])) return "repo/edit";
    if (["rg", "grep", "find", "fd"].includes(words[0])) return "code search";
    if (["cat", "sed", "head", "tail", "awk", "ls", "pwd"].includes(words[0])) return "file read";
    if (["go", "pnpm", "npm", "yarn", "make", "cargo", "pytest", "jest", "vitest"].includes(words[0])) return "build/test";
    if (["python", "python3", "node", "ruby", "perl", "sqlite3", "psql", "mysql"].includes(words[0])) return "script/query";
    if (["tmux", "sudo", "curl", "wget", "df", "du", "ss"].includes(words[0])) return "system";
    return "other";
  }
  return "other";
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
  for (const multiple of [1, 1.25, 1.5, 2, 2.5, 5, 10]) {
    const ceiling = multiple * magnitude;
    if (ceiling >= target) return ceiling;
  }
  return target;
}
</script>
