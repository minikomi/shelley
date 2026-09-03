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
          { 'conversation-query-text-bare-modifier': view.bareModifierKind },
          { 'conversation-query-text-bare-tag': view.bareModifierKind === 'tag' },
          { 'conversation-query-text-bare-user': view.bareModifierKind === 'user' },
        ]"
        :style="{ width: view.width }"
        :value="view.display"
        :placeholder="views.length === 1 ? placeholder : ''"
        :aria-label="view.primary ? ariaLabelText : `${ariaLabelText} text segment`"
        data-testid="conversation-query-text"
        :data-structured-edit-kind="view.editingKind"
        autocomplete="off"
        autocapitalize="none"
        spellcheck="false"
        @input="updateText(view, $event)"
        @keydown="onTextKeyDown(view, $event)"
        @blur="onTextBlur(view)"
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
import { computed, nextTick, onBeforeUpdate, ref } from "vue";
import {
  activeStructuredQueryEdit,
  bareStructuredModifierAtCaret,
  isBareStructuredQueryEdit,
  removeConversationQueryTerm,
  removeConversationQueryToken,
  replaceStructuredQueryEdit,
  scanConversationQuery,
  tokenizeConversationQuery,
  type ActiveStructuredQueryEdit,
  type ConversationQueryToken,
  type EditableStructuredQueryKind,
  type QueryTextToken,
  type StructuredQueryEdit,
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
  (event: "structured-edit-change", value: ActiveStructuredQueryEdit | null): void;
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
  editingKind?: EditableStructuredQueryKind;
  bareModifierKind?: EditableStructuredQueryKind;
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

interface EditingTextToken extends QueryTextToken {
  editingKind: EditableStructuredQueryKind;
}

interface BareModifierTextToken extends QueryTextToken {
  bareModifierKind: EditableStructuredQueryKind;
}

type ViewToken = ConversationQueryToken | EditingTextToken | BareModifierTextToken;

const editingTerm = ref<StructuredQueryEdit | null>(null);
let restoringEditingCaret = false;

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

function offsetToken(token: ConversationQueryToken, offset: number): ConversationQueryToken {
  return { ...token, start: token.start + offset, end: token.end + offset };
}

function viewTokens(): ViewToken[] {
  const edit = editingTerm.value;
  if (!edit || edit.start < 0 || edit.end < edit.start || edit.end > props.modelValue.length) {
    const tokens: ViewToken[] = tokenizeConversationQuery(props.modelValue);
    const scanned = scanConversationQuery(props.modelValue);
    const term = scanned.endsMidTerm ? scanned.terms[scanned.terms.length - 1] : undefined;
    const lower = term?.raw.toLowerCase();
    const kind: EditableStructuredQueryKind | null =
      lower === "tag:" ? "tag" : lower === "user:" ? "user" : null;
    if (!term || !kind) return tokens;
    const index = tokens.findIndex(
      (token) => token.kind === "text" && term.start >= token.start && term.end <= token.end,
    );
    if (index < 0) return tokens;
    const token = tokens[index] as QueryTextToken;
    return [
      ...tokens.slice(0, index),
      {
        kind: "text",
        raw: props.modelValue.slice(token.start, term.start),
        start: token.start,
        end: term.start,
      },
      {
        kind: "text",
        raw: term.raw,
        start: term.start,
        end: term.end,
        bareModifierKind: kind,
      },
      ...tokens.slice(index + 1),
    ];
  }
  return [
    ...tokenizeConversationQuery(props.modelValue.slice(0, edit.start)),
    {
      kind: "text",
      raw: props.modelValue.slice(edit.start, edit.end),
      start: edit.start,
      end: edit.end,
      editingKind: edit.kind,
    },
    ...tokenizeConversationQuery(props.modelValue.slice(edit.end)).map((token) =>
      offsetToken(token, edit.end),
    ),
  ];
}

function isStructuredBoundary(token: ViewToken | undefined): boolean {
  return token !== undefined && (token.kind !== "text" || "editingKind" in token);
}

const views = computed<EditorView[]>(() => {
  const tokens = viewTokens();
  let primaryIndex = -1;
  for (let index = tokens.length - 1; index >= 0; index -= 1) {
    if (tokens[index].kind === "text" && !("editingKind" in tokens[index])) {
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
    if ("editingKind" in token) {
      const bareModifierKind =
        token.raw.toLowerCase() === `${token.editingKind}:` ? token.editingKind : undefined;
      return {
        kind: "text",
        key: `editing:${token.start}`,
        index,
        start: token.start,
        end: token.end,
        display: token.raw,
        leading: "",
        trailing: "",
        previousStructured: false,
        nextStructured: false,
        primary: false,
        collapsed: false,
        width: `${Math.max(token.raw.length + (bareModifierKind ? 2 : 0.75), 0.75)}ch`,
        editingKind: token.editingKind,
        bareModifierKind,
      };
    }
    if ("bareModifierKind" in token) {
      return {
        kind: "text",
        key: `bare:${token.start}`,
        index,
        start: token.start,
        end: token.end,
        display: token.raw,
        leading: "",
        trailing: "",
        previousStructured: false,
        nextStructured: false,
        primary: index === primaryIndex,
        collapsed: false,
        width: `${token.raw.length + 2}ch`,
        bareModifierKind: token.bareModifierKind,
      };
    }
    const previousStructured = isStructuredBoundary(tokens[index - 1]);
    const nextStructured = isStructuredBoundary(tokens[index + 1]);
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
  if (view.editingKind) {
    const edit = editingTerm.value;
    if (!edit || edit.start !== view.start || edit.end !== view.end) return;
    const query =
      props.modelValue.slice(0, edit.start) + input.value + props.modelValue.slice(edit.end);
    const nextEdit = { kind: edit.kind, start: edit.start, end: edit.start + input.value.length };
    const displayCaret = input.selectionStart ?? input.value.length;
    editingTerm.value = nextEdit;
    restoringEditingCaret = true;
    emit("update:modelValue", query);
    emit("structured-edit-change", activeStructuredQueryEdit(query, nextEdit));
    void nextTick(() => {
      const target = views.value.find(
        (candidate): candidate is TextView =>
          candidate.kind === "text" &&
          candidate.editingKind !== undefined &&
          candidate.start === nextEdit.start,
      );
      const element = target ? inputRefs.get(target.index) : undefined;
      if (element) {
        element.focus();
        element.setSelectionRange(displayCaret, displayCaret);
      }
      restoringEditingCaret = false;
    });
    return;
  }
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
    let target = textViews.find(
      (view) => view.editingKind && rawOffset >= view.start && rawOffset <= view.end,
    );
    target ??=
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

function finishStructuredEdit() {
  if (!editingTerm.value) return;
  editingTerm.value = null;
  emit("structured-edit-change", null);
}

function removeTerm(start: number, end: number) {
  const edit = editingTerm.value;
  if (edit && start <= edit.end && end >= edit.start) finishStructuredEdit();
  const removed = removeConversationQueryTerm(props.modelValue, start, end);
  emit("update:modelValue", removed.query);
  restoreCaret(removed.caret);
}

function startEditingToken(view: StructuredView, atEnd: boolean) {
  if (view.kind !== "tag" && view.kind !== "user") {
    removeToken(view.start);
    return;
  }
  const edit = { kind: view.kind, start: view.start, end: view.end };
  editingTerm.value = edit;
  emit("structured-edit-change", activeStructuredQueryEdit(props.modelValue, edit));
  void nextTick(() => {
    const target = views.value.find(
      (candidate): candidate is TextView =>
        candidate.kind === "text" &&
        candidate.editingKind !== undefined &&
        candidate.start === edit.start,
    );
    if (target) focusText(target.index, atEnd);
  });
}

function rawDisplayOffset(view: TextView, displayOffset: number): number {
  return view.start + view.leading.length + displayOffset;
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
    start === end &&
    view.editingKind &&
    editingTerm.value &&
    isBareStructuredQueryEdit(props.modelValue, editingTerm.value) &&
    (event.key === "Backspace" || event.key === "Delete")
  ) {
    event.preventDefault();
    removeTerm(editingTerm.value.start, editingTerm.value.end);
    return;
  }
  if (start === end && (event.key === "Backspace" || event.key === "Delete")) {
    const direction = event.key === "Backspace" ? "backward" : "forward";
    const modifier = bareStructuredModifierAtCaret(
      props.modelValue,
      rawDisplayOffset(view, start),
      direction,
    );
    if (modifier) {
      event.preventDefault();
      removeTerm(modifier.start, modifier.end);
      return;
    }
  }
  if (
    event.key === "Backspace" &&
    start === end &&
    beforeIsBoundary &&
    previous?.kind !== undefined &&
    previous.kind !== "text"
  ) {
    event.preventDefault();
    startEditingToken(previous, true);
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
    startEditingToken(next, false);
    return;
  }
  if (view.editingKind && event.key === "ArrowLeft" && start === end && start === 0) {
    event.preventDefault();
    const rawOffset = view.start;
    finishStructuredEdit();
    restoreCaret(rawOffset);
    return;
  }
  if (
    view.editingKind &&
    event.key === "ArrowRight" &&
    start === end &&
    end === input.value.length
  ) {
    event.preventDefault();
    const rawOffset = view.end;
    finishStructuredEdit();
    restoreCaret(rawOffset);
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

function onTextBlur(view: TextView) {
  if (!view.editingKind) return;
  if (restoringEditingCaret) return;
  void nextTick(() => {
    const current = views.value.find(
      (candidate): candidate is TextView =>
        candidate.kind === "text" &&
        candidate.editingKind !== undefined &&
        candidate.start === view.start,
    );
    if (current && document.activeElement === inputRefs.get(current.index)) return;
    finishStructuredEdit();
  });
}

function completeStructuredTerm(term: string): boolean {
  const edit = editingTerm.value;
  if (!edit) return false;
  const completed = replaceStructuredQueryEdit(props.modelValue, edit, term);
  finishStructuredEdit();
  emit("update:modelValue", completed.query);
  restoreCaret(completed.caret);
  return true;
}

function focusEnd() {
  const target = [...views.value].reverse().find((view): view is TextView => view.kind === "text");
  if (target) focusText(target.index, true);
}

function focusEditorBackground(event: MouseEvent) {
  if (event.target === event.currentTarget) focusEnd();
}

defineExpose({ completeStructuredTerm, finishStructuredEdit, focusEnd });
</script>
