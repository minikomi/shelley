<template>
  <div
    class="conversation-query-editor"
    data-testid="conversation-query-editor"
    @click="focusEditorBackground"
  >
    <template v-for="view in views" :key="view.key">
      <input
        v-if="view.kind === 'text'"
        :ref="(element) => setInputRef(view.index, element)"
        type="text"
        :class="[
          'conversation-query-text',
          { 'drawer-search-input': view.primary },
          { 'conversation-query-text-primary': view.primary },
          { 'conversation-query-text-collapsed': view.collapsed },
        ]"
        :style="{ width: view.width }"
        :value="view.display"
        :placeholder="views.length === 1 ? placeholder : ''"
        :aria-label="view.primary ? ariaLabelText : `${ariaLabelText} text segment`"
        data-testid="conversation-query-text"
        autocomplete="off"
        spellcheck="false"
        @input="updateText(view, $event)"
        @keydown="onTextKeyDown(view, $event)"
      />
      <span
        v-else
        :class="[
          'drawer-filter-token',
          `drawer-filter-token-${view.kind}`,
          { 'drawer-filter-token-participant': view.kind === 'user' },
          {
            'drawer-filter-token-tag': view.kind === 'tag' || view.kind === 'untagged',
          },
          { 'drawer-filter-token-unattributed': view.kind === 'unattributed' },
        ]"
        :data-testid="view.testId"
        :data-query-token-start="view.start"
      >
        <span class="drawer-filter-token-label">{{ view.label }}</span>
        <button
          type="button"
          :aria-label="`Remove ${view.label} filter`"
          @click="removeToken(view.start)"
        >
          ×
        </button>
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUpdate } from "vue";
import {
  removeConversationQueryToken,
  tokenizeConversationQuery,
  type QueryTextToken,
  type StructuredQueryKind,
} from "../../utils/conversationQuery";

const props = defineProps<{
  modelValue: string;
  placeholder: string;
  ariaLabelText: string;
}>();

const emit = defineEmits<{
  (event: "update:modelValue", value: string): void;
  (event: "keydown", value: KeyboardEvent): void;
}>();

interface TextView {
  kind: "text";
  key: string;
  index: number;
  start: number;
  end: number;
  display: string;
  leading: string;
  trailing: string;
  previousStructured: boolean;
  nextStructured: boolean;
  primary: boolean;
  collapsed: boolean;
  width: string;
}

interface StructuredView {
  kind: StructuredQueryKind;
  key: string;
  index: number;
  start: number;
  end: number;
  label: string;
  testId: string;
}

type EditorView = TextView | StructuredView;

function tokenTestId(kind: StructuredQueryKind): string {
  if (kind === "user") return "user-filter-token";
  if (kind === "tag") return "selected-tag-filter";
  if (kind === "untagged") return "untagged-filter-token";
  return "unattributed-filter-token";
}

function splitTextToken(
  token: QueryTextToken,
  previousStructured: boolean,
  nextStructured: boolean,
): Pick<TextView, "display" | "leading" | "trailing"> {
  const leading =
    previousStructured || token.start === 0 ? (/^\s*/.exec(token.raw)?.[0] ?? "") : "";
  let trailing =
    nextStructured || token.end === props.modelValue.length
      ? (/\s*$/.exec(token.raw)?.[0] ?? "")
      : "";
  if (leading.length + trailing.length > token.raw.length) trailing = "";
  return {
    leading,
    trailing,
    display: token.raw.slice(leading.length, token.raw.length - trailing.length),
  };
}

const views = computed<EditorView[]>(() => {
  const tokens = tokenizeConversationQuery(props.modelValue);
  let primaryIndex = -1;
  for (let index = tokens.length - 1; index >= 0; index -= 1) {
    if (tokens[index].kind === "text") {
      primaryIndex = index;
      break;
    }
  }
  return tokens.map((token, index) => {
    if (token.kind !== "text") {
      return {
        kind: token.kind,
        key: `structured:${token.start}:${token.raw}`,
        index,
        start: token.start,
        end: token.end,
        label: token.label,
        testId: tokenTestId(token.kind),
      };
    }
    const previousStructured = index > 0 && tokens[index - 1].kind !== "text";
    const nextStructured = index + 1 < tokens.length && tokens[index + 1].kind !== "text";
    const split = splitTextToken(token, previousStructured, nextStructured);
    return {
      kind: "text",
      key: `text:${index}:${token.start}`,
      index,
      start: token.start,
      end: token.end,
      ...split,
      previousStructured,
      nextStructured,
      primary: index === primaryIndex,
      collapsed: split.display === "" && !previousStructured && nextStructured,
      width: `${Math.max(split.display.length + 0.75, index === primaryIndex ? 3 : 0.75)}ch`,
    };
  });
});

const inputRefs = new Map<number, HTMLInputElement>();

onBeforeUpdate(() => inputRefs.clear());

function setInputRef(index: number, element: unknown) {
  if (element instanceof HTMLInputElement) inputRefs.set(index, element);
}

function rebuildText(
  view: TextView,
  display: string,
): {
  raw: string;
  displayStart: number;
} {
  if (display === "" && view.previousStructured && view.nextStructured) {
    return { raw: view.leading || view.trailing || " ", displayStart: 0 };
  }
  const leading = view.previousStructured ? view.leading || " " : "";
  const trailing = view.nextStructured ? view.trailing || " " : "";
  return { raw: leading + display + trailing, displayStart: leading.length };
}

function updateText(view: TextView, event: Event) {
  const input = event.target as HTMLInputElement;
  const rebuilt = rebuildText(view, input.value);
  const query =
    props.modelValue.slice(0, view.start) + rebuilt.raw + props.modelValue.slice(view.end);
  const rawCaret = view.start + rebuilt.displayStart + (input.selectionStart ?? input.value.length);
  emit("update:modelValue", query);
  restoreCaret(rawCaret);
}

function restoreCaret(rawOffset: number) {
  void nextTick(() => {
    const textViews = views.value.filter((view): view is TextView => view.kind === "text");
    let target =
      textViews.find((view) => rawOffset >= view.start && rawOffset <= view.end) ??
      textViews[textViews.length - 1];
    if (!target) return;

    // At a shared boundary, prefer the segment after a pill.
    const following = textViews.find((view) => view.start === rawOffset && view.previousStructured);
    if (following) target = following;

    const input = inputRefs.get(target.index);
    if (!input) return;
    const displayOffset = Math.max(
      0,
      Math.min(target.display.length, rawOffset - target.start - target.leading.length),
    );
    input.focus();
    input.setSelectionRange(displayOffset, displayOffset);
  });
}

function focusText(index: number, atEnd: boolean) {
  const input = inputRefs.get(index);
  if (!input) return;
  const offset = atEnd ? input.value.length : 0;
  input.focus();
  input.setSelectionRange(offset, offset);
}

function removeToken(start: number) {
  const removed = removeConversationQueryToken(props.modelValue, start);
  emit("update:modelValue", removed.query);
  restoreCaret(removed.caret);
}

function onTextKeyDown(view: TextView, event: KeyboardEvent) {
  const input = event.currentTarget as HTMLInputElement;
  const start = input.selectionStart ?? 0;
  const end = input.selectionEnd ?? start;
  const previous = views.value[view.index - 1];
  const next = views.value[view.index + 1];
  const beforeIsBoundary = input.value.slice(0, start).trim() === "";
  const afterIsBoundary = input.value.slice(end).trim() === "";

  if (
    event.key === "Backspace" &&
    start === end &&
    beforeIsBoundary &&
    previous?.kind !== undefined &&
    previous.kind !== "text"
  ) {
    event.preventDefault();
    removeToken(previous.start);
    return;
  }
  if (
    event.key === "Delete" &&
    start === end &&
    afterIsBoundary &&
    next?.kind !== undefined &&
    next.kind !== "text"
  ) {
    event.preventDefault();
    removeToken(next.start);
    return;
  }
  if (
    event.key === "ArrowLeft" &&
    start === end &&
    beforeIsBoundary &&
    previous?.kind !== undefined &&
    previous.kind !== "text"
  ) {
    event.preventDefault();
    focusText(view.index - 2, true);
    return;
  }
  if (
    event.key === "ArrowRight" &&
    start === end &&
    afterIsBoundary &&
    next?.kind !== undefined &&
    next.kind !== "text"
  ) {
    event.preventDefault();
    focusText(view.index + 2, false);
    return;
  }
  emit("keydown", event);
}

function focusEnd() {
  const target = [...views.value].reverse().find((view): view is TextView => view.kind === "text");
  if (target) focusText(target.index, true);
}

function focusEditorBackground(event: MouseEvent) {
  if (event.target === event.currentTarget) focusEnd();
}

defineExpose({ focusEnd });
</script>
