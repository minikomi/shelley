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
        autocapitalize="none"
        spellcheck="false"
        @input="updateText(view, $event)"
        @keydown="onTextKeyDown(view, $event)"
      />
      <span
        v-else-if="view.kind === 'tag' || view.kind === 'user'"
        :class="[
          'drawer-filter-token',
          'conversation-query-filter',
          `drawer-filter-token-${view.kind}`,
          { 'drawer-filter-token-participant': view.kind === 'user' },
          { 'drawer-filter-token-tag': view.kind === 'tag' },
          { 'conversation-query-filter-primary': view.primary },
        ]"
        :data-testid="view.testId"
        :data-query-token-start="view.start"
      >
        <span
          class="conversation-query-filter-keyword"
          data-testid="conversation-query-keyword"
          @mousedown="onKeywordMouseDown(view, $event)"
        >
          {{ view.keyword }}
        </span>
        <input
          v-if="view.editable"
          :ref="(element) => setInputRef(view.index, element)"
          type="text"
          :class="[
            'conversation-query-filter-value-input',
            { 'drawer-search-input': view.primary },
          ]"
          :style="{ width: view.width }"
          :value="view.value"
          :aria-label="view.primary ? ariaLabelText : `${view.keyword} filter value`"
          data-testid="conversation-query-value"
          :data-structured-edit-kind="view.kind"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          @focus="onStructuredFocus(view)"
          @input="updateStructuredValue(view, $event)"
          @keydown="onStructuredKeyDown(view, $event)"
          @blur="onStructuredBlur(view)"
        />
        <span v-else class="conversation-query-filter-value">{{ view.value }}</span>
        <button
          type="button"
          :aria-label="`Remove ${view.label} filter`"
          @mousedown.prevent
          @click.stop="removeStructuredView(view)"
        >
          ×
        </button>
      </span>
      <span
        v-else
        :class="[
          'drawer-filter-token',
          `drawer-filter-token-${view.kind}`,
          { 'drawer-filter-token-tag': view.kind === 'untagged' },
          { 'drawer-filter-token-unattributed': view.kind === 'unattributed' },
        ]"
        :data-testid="view.testId"
        :data-query-token-start="view.start"
      >
        <span class="drawer-filter-token-label">{{ view.label }}</span>
        <button
          type="button"
          :aria-label="`Remove ${view.label} filter`"
          @click="removeTerm(view.start, view.end)"
        >
          ×
        </button>
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUpdate, ref, watch } from "vue";
import {
  activeStructuredQueryEdit,
  bareStructuredModifierAtCaret,
  removeConversationQueryTerm,
  replaceStructuredQueryEdit,
  scanConversationQuery,
  tokenizeConversationQuery,
  type ActiveStructuredQueryEdit,
  type EditableStructuredQueryKind,
  type QueryTextToken,
  type StructuredQueryEdit,
  type StructuredQueryKind,
  type StructuredQueryToken,
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
}

interface FilterView {
  kind: EditableStructuredQueryKind;
  key: string;
  index: number;
  start: number;
  end: number;
  keyword: string;
  value: string;
  label: string;
  testId: string;
  editable: boolean;
  activeEdit: boolean;
  trailingPartial: boolean;
  primary: boolean;
  width: string;
}

interface StateView {
  kind: Exclude<StructuredQueryKind, EditableStructuredQueryKind>;
  key: string;
  index: number;
  start: number;
  end: number;
  label: string;
  testId: string;
}

type EditorView = TextView | FilterView | StateView;

interface ViewStructuredToken {
  kind: StructuredQueryKind;
  raw: string;
  start: number;
  end: number;
  label: string;
  value: string;
  editable: boolean;
  activeEdit: boolean;
  trailingPartial: boolean;
}

type ViewToken = QueryTextToken | ViewStructuredToken;

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

function editableKind(raw: string): EditableStructuredQueryKind | null {
  const lower = raw.toLowerCase();
  if (lower.startsWith("tag:")) return "tag";
  if (lower.startsWith("user:")) return "user";
  return null;
}

function keywordForKind(kind: EditableStructuredQueryKind): string {
  return `${kind}:`;
}

function isValidEdit(edit: StructuredQueryEdit | null): edit is StructuredQueryEdit {
  if (
    edit === null ||
    edit.start < 0 ||
    edit.end < edit.start ||
    edit.end > props.modelValue.length
  ) {
    return false;
  }
  const keyword = keywordForKind(edit.kind);
  return props.modelValue.slice(edit.start, edit.start + keyword.length).toLowerCase() === keyword;
}

function viewTokens(): ViewToken[] {
  const raw = props.modelValue;
  const edit = editingTerm.value;
  const validEdit = isValidEdit(edit) ? edit : null;
  const structured: ViewStructuredToken[] = tokenizeConversationQuery(raw)
    .filter((token): token is StructuredQueryToken => token.kind !== "text")
    .filter((token) => !validEdit || token.end <= validEdit.start || token.start >= validEdit.end)
    .map((token) => ({
      ...token,
      editable: false,
      activeEdit: false,
      trailingPartial: false,
    }));

  const scanned = scanConversationQuery(raw);
  scanned.terms.forEach((term, index) => {
    if (validEdit && term.start < validEdit.end && term.end > validEdit.start) {
      return;
    }
    const kind = editableKind(term.raw);
    if (!kind) return;
    const bare = term.raw.toLowerCase() === keywordForKind(kind);
    const trailingPartial = scanned.endsMidTerm && index === scanned.terms.length - 1;
    if (!bare && !trailingPartial) return;
    if (structured.some((token) => token.start === term.start)) return;
    structured.push({
      kind,
      raw: term.raw,
      start: term.start,
      end: term.end,
      label: `${keywordForKind(kind)}${term.raw.slice(keywordForKind(kind).length)}`,
      value: term.raw.slice(keywordForKind(kind).length),
      editable: true,
      activeEdit: false,
      trailingPartial,
    });
  });

  if (validEdit) {
    const rawTerm = raw.slice(validEdit.start, validEdit.end);
    const keyword = keywordForKind(validEdit.kind);
    structured.push({
      kind: validEdit.kind,
      raw: rawTerm,
      start: validEdit.start,
      end: validEdit.end,
      label: `${keyword}${rawTerm.slice(keyword.length)}`,
      value: rawTerm.slice(keyword.length),
      editable: true,
      activeEdit: true,
      trailingPartial: false,
    });
  }

  structured.sort((left, right) => left.start - right.start);
  const tokens: ViewToken[] = [];
  let textStart = 0;
  for (const token of structured) {
    if (token.start < textStart) continue;
    tokens.push({
      kind: "text",
      raw: raw.slice(textStart, token.start),
      start: textStart,
      end: token.start,
    });
    tokens.push(token);
    textStart = token.end;
  }
  tokens.push({
    kind: "text",
    raw: raw.slice(textStart),
    start: textStart,
    end: raw.length,
  });
  return tokens;
}

function isStructuredBoundary(token: ViewToken | undefined): boolean {
  return token !== undefined && token.kind !== "text";
}

const views = computed<EditorView[]>(() => {
  const tokens = viewTokens();
  const trailingPartialIndex = tokens.findIndex(
    (token) => token.kind !== "text" && token.trailingPartial,
  );
  let primaryIndex = -1;
  if (trailingPartialIndex < 0) {
    for (let index = tokens.length - 1; index >= 0; index -= 1) {
      if (tokens[index].kind === "text") {
        primaryIndex = index;
        break;
      }
    }
  }
  return tokens.map((token, index) => {
    if (token.kind !== "text") {
      if (token.kind === "tag" || token.kind === "user") {
        const keyword = keywordForKind(token.kind);
        const value = token.editable ? token.raw.slice(keyword.length) : token.value;
        return {
          kind: token.kind,
          key: `filter:${token.start}:${token.kind}`,
          index,
          start: token.start,
          end: token.end,
          keyword,
          value,
          label: token.editable ? `${keyword}${value}` : token.label,
          testId: tokenTestId(token.kind),
          editable: token.editable,
          activeEdit: token.activeEdit,
          trailingPartial: token.trailingPartial,
          primary: index === trailingPartialIndex,
          width: `${Math.max(value.length + 0.75, 0.75)}ch`,
        };
      }
      return {
        kind: token.kind,
        key: `state:${token.start}:${token.kind}`,
        index,
        start: token.start,
        end: token.end,
        label: token.label,
        testId: tokenTestId(token.kind),
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
  const rebuilt = rebuildText(view, input.value);
  const query =
    props.modelValue.slice(0, view.start) + rebuilt.raw + props.modelValue.slice(view.end);
  const rawCaret = view.start + rebuilt.displayStart + (input.selectionStart ?? input.value.length);
  emit("update:modelValue", query);
  restoreCaret(rawCaret);
}

function updateStructuredValue(view: FilterView, event: Event) {
  const input = event.target as HTMLInputElement;
  const keywordLength = view.keyword.length;
  const rawKeyword = props.modelValue.slice(view.start, view.start + keywordLength);
  const replacement = rawKeyword + input.value;
  const query =
    props.modelValue.slice(0, view.start) + replacement + props.modelValue.slice(view.end);
  const valueCaret = input.selectionStart ?? input.value.length;
  const rawCaret = view.start + keywordLength + valueCaret;
  const edit = editingTerm.value;
  if (edit && edit.start === view.start && edit.kind === view.kind) {
    const nextEdit = {
      kind: edit.kind,
      start: edit.start,
      end: edit.start + replacement.length,
    };
    editingTerm.value = nextEdit;
    emit("structured-edit-change", activeStructuredQueryEdit(query, nextEdit));
  }
  restoringEditingCaret = true;
  emit("update:modelValue", query);
  void nextTick(() => {
    focusRawOffset(rawCaret);
    restoringEditingCaret = false;
  });
}

function focusRawOffset(rawOffset: number) {
  const inputViews = views.value.filter(
    (view): view is TextView | FilterView =>
      view.kind === "text" || ((view.kind === "tag" || view.kind === "user") && view.editable),
  );
  let target = inputViews.find(
    (view) =>
      view.kind !== "text" && view.editable && rawOffset >= view.start && rawOffset <= view.end,
  );
  target ??=
    inputViews.find((view) => rawOffset >= view.start && rawOffset <= view.end) ??
    inputViews[inputViews.length - 1];
  if (!target) return;

  // At a shared boundary, prefer the text segment after a filter.
  const following = inputViews.find(
    (view) => view.kind === "text" && view.start === rawOffset && view.previousStructured,
  );
  if (following && target.kind === "text") target = following;

  const input = inputRefs.get(target.index);
  if (!input) return;
  const displayOffset =
    target.kind === "text"
      ? Math.max(
          0,
          Math.min(target.display.length, rawOffset - target.start - target.leading.length),
        )
      : Math.max(
          0,
          Math.min(target.value.length, rawOffset - target.start - target.keyword.length),
        );
  input.focus();
  input.setSelectionRange(displayOffset, displayOffset);
}

function restoreCaret(rawOffset: number) {
  void nextTick(() => focusRawOffset(rawOffset));
}

function focusInput(index: number, atEnd: boolean) {
  const input = inputRefs.get(index);
  if (!input) return;
  const offset = atEnd ? input.value.length : 0;
  input.focus();
  input.setSelectionRange(offset, offset);
}

function finishStructuredEdit() {
  if (!editingTerm.value) return;
  editingTerm.value = null;
  emit("structured-edit-change", null);
}

function removeTerm(start: number, end: number) {
  finishStructuredEdit();
  const removed = removeConversationQueryTerm(props.modelValue, start, end);
  emit("update:modelValue", removed.query);
  restoreCaret(removed.caret);
}

function removeStructuredView(view: FilterView) {
  removeTerm(view.start, view.end);
}

function startEditingToken(view: FilterView | StateView, atEnd: boolean) {
  if (view.kind !== "tag" && view.kind !== "user") {
    removeTerm(view.start, view.end);
    return;
  }
  if (!view.editable) {
    const edit = { kind: view.kind, start: view.start, end: view.end };
    editingTerm.value = edit;
    emit("structured-edit-change", activeStructuredQueryEdit(props.modelValue, edit));
  }
  void nextTick(() => {
    const target = views.value.find(
      (candidate): candidate is FilterView =>
        (candidate.kind === "tag" || candidate.kind === "user") &&
        candidate.editable &&
        candidate.start === view.start,
    );
    if (target) focusInput(target.index, atEnd);
  });
}

function rawDisplayOffset(view: TextView, displayOffset: number): number {
  return view.start + view.leading.length + displayOffset;
}

function isEditableFilter(view: EditorView | undefined): view is FilterView {
  return view !== undefined && (view.kind === "tag" || view.kind === "user") && view.editable;
}

function onTextKeyDown(view: TextView, event: KeyboardEvent) {
  const input = event.currentTarget as HTMLInputElement;
  const start = input.selectionStart ?? 0;
  const end = input.selectionEnd ?? start;
  const previous = views.value[view.index - 1];
  const next = views.value[view.index + 1];
  const beforeIsBoundary = input.value.slice(0, start).trim() === "";
  const afterIsBoundary = input.value.slice(end).trim() === "";

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
  if (
    event.key === "ArrowLeft" &&
    start === end &&
    beforeIsBoundary &&
    previous?.kind !== undefined &&
    previous.kind !== "text"
  ) {
    event.preventDefault();
    focusInput(isEditableFilter(previous) ? view.index - 1 : view.index - 2, true);
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
    focusInput(isEditableFilter(next) ? view.index + 1 : view.index + 2, false);
    return;
  }
  emit("keydown", event);
}

function onStructuredFocus(view: FilterView) {
  if (view.activeEdit || view.trailingPartial) return;
  const edit = { kind: view.kind, start: view.start, end: view.end };
  editingTerm.value = edit;
  emit("structured-edit-change", activeStructuredQueryEdit(props.modelValue, edit));
}

function onKeywordMouseDown(view: FilterView, event: MouseEvent) {
  if (!view.editable) return;
  event.preventDefault();
  focusInput(view.index, true);
}

function onStructuredKeyDown(view: FilterView, event: KeyboardEvent) {
  const input = event.currentTarget as HTMLInputElement;
  const start = input.selectionStart ?? 0;
  const end = input.selectionEnd ?? start;
  if (start === end && view.value === "" && (event.key === "Backspace" || event.key === "Delete")) {
    event.preventDefault();
    removeTerm(view.start, view.end);
    return;
  }
  if (event.key === "ArrowLeft" && start === end && start === 0) {
    event.preventDefault();
    finishStructuredEdit();
    void nextTick(() => focusInput(view.index - 1, true));
    return;
  }
  if (
    event.key === "ArrowRight" &&
    start === end &&
    end === input.value.length &&
    view.end < props.modelValue.length
  ) {
    event.preventDefault();
    finishStructuredEdit();
    void nextTick(() => focusInput(view.index + 1, false));
    return;
  }
  emit("keydown", event);
}

function onStructuredBlur(view: FilterView) {
  const ownsEdit = (edit: StructuredQueryEdit | null) =>
    edit?.kind === view.kind && edit.start === view.start;
  if (!ownsEdit(editingTerm.value) || restoringEditingCaret) return;
  void nextTick(() => {
    if (!ownsEdit(editingTerm.value)) return;
    const current = views.value.find(
      (candidate): candidate is FilterView =>
        (candidate.kind === "tag" || candidate.kind === "user") &&
        candidate.editable &&
        candidate.start === view.start,
    );
    if (current && document.activeElement === inputRefs.get(current.index)) return;
    finishStructuredEdit();
  });
}

watch(
  () => props.modelValue,
  () => {
    if (editingTerm.value && !isValidEdit(editingTerm.value)) finishStructuredEdit();
  },
);

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
  const target =
    views.value.find(
      (view): view is TextView | FilterView =>
        (view.kind === "text" ||
          ((view.kind === "tag" || view.kind === "user") && view.editable)) &&
        view.primary,
    ) ??
    [...views.value]
      .reverse()
      .find(
        (view): view is TextView | FilterView =>
          view.kind === "text" || ((view.kind === "tag" || view.kind === "user") && view.editable),
      );
  if (target) focusInput(target.index, true);
}

function focusEditorBackground(event: MouseEvent) {
  if (event.target === event.currentTarget) focusEnd();
}

defineExpose({ completeStructuredTerm, finishStructuredEdit, focusEnd });
</script>
