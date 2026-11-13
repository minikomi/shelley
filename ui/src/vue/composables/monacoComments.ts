// Shared comment-mode UX for Monaco-based viewers (DiffViewer and
// EditableFileModal). In comment mode the editor is read-only; clicking a
// line (or selecting text and clicking the floating "Comment" prompt) opens
// a dialog whose submitted text is turned into a quoted block
// ("> file:line: code\ncomment\n\n") and handed to the host, which injects it
// into the message input.
//
// Extracted from DiffViewer.vue so EditableFileModal can offer the same
// edit/comment mode toggle. The dialog markup itself lives in
// CommentDialog.vue; hosts render it (plus the floating prompt button) bound
// to the refs returned here.
import { ref } from "vue";
import type * as Monaco from "monaco-editor";

// How a commit highlight is quoted when a comment answers it.
export function highlightQuote(highlight: string): string {
  return `\u2605 ${highlight}`;
}

export interface CommentDialogInfo {
  line: number;
  side: "left" | "right";
  selectedText?: string;
  startLine?: number;
  endLine?: number;
  /** A commit highlight's comment being answered, quoted with the line. */
  highlight?: string;
}

/**
 * CommentDialog's "what am I commenting on" label for a line comment.
 *
 * `showSide` distinguishes the diff views, where a line number is ambiguous
 * between the old and new file, from the single-file editor, where it isn't.
 */
export function lineCommentLabel(info: CommentDialogInfo, showSide: boolean): string {
  const lines =
    info.startLine !== info.endLine
      ? `Lines ${info.startLine}-${info.endLine}`
      : `Line ${info.line}`;
  return showSide ? `${lines}, ${info.side === "left" ? "old" : "new"}` : lines;
}

// Floating "add comment" prompt shown after a text selection in comment mode.
// Lets the user keep their selection (rather than the dialog popping up
// immediately on click and interfering with selecting text).
export interface CommentPromptInfo {
  top: number;
  left: number;
  startLine: number;
  endLine: number;
  selectedText: string;
}

export interface MonacoCommentsDeps {
  monaco: () => typeof Monaco | null;
  /** Current interaction mode; all handlers are inert unless "comment". */
  mode: () => "comment" | "edit" | "preview" | "split";
  isMobile: () => boolean;
  /** Element the floating prompt is positioned relative to. */
  promptHost: () => HTMLElement | null;
  /**
   * Reference used in the quote header of a submitted comment, e.g. a
   * repo-relative path or "commit abc123 (subject)". Null aborts the submit.
   */
  fileRef: () => string | null;
  onSubmit: (block: string) => void;
}

export function truncateWithEllipsis(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return text.slice(0, Math.max(0, maxLength - 3)) + "...";
}

export function useMonacoComments(deps: MonacoCommentsDeps) {
  const showCommentDialog = ref<CommentDialogInfo | null>(null);
  const commentPrompt = ref<CommentPromptInfo | null>(null);
  const commentText = ref("");
  // Bumped every time the dialog is pointed at something. Hosts key the dialog
  // on it so retargeting remounts it -- which is what recenters and refocuses
  // it -- including when the new target's label matches the old one (clicking
  // the same line twice).
  const commentDialogOpens = ref(0);

  function pointDialogAt(info: CommentDialogInfo) {
    showCommentDialog.value = info;
    commentDialogOpens.value++;
  }

  /**
   * Wire comment-mode mouse/touch/hover handlers onto an editor. `editorDom`
   * is the host element the (mobile) touch listeners attach to. Returns a
   * cleanup function; call it before disposing the editor.
   */
  function attach(editor: Monaco.editor.IStandaloneCodeEditor, editorDom: HTMLElement): () => void {
    const monaco = deps.monaco();
    if (!monaco) return () => {};

    let hoverDecorations: string[] = [];
    // Desktop: where a comment-mode mousedown started, to distinguish click vs drag.
    let mouseDownPos: { x: number; y: number } | null = null;
    // Mobile: tap-without-scroll tracking.
    let touchScrolled = false;
    let touchStartPos: {
      x: number;
      y: number;
      id: number;
      time: number;
      scrollTop: number;
      scrollLeft: number;
    } | null = null;
    const disposables: Monaco.IDisposable[] = [];

    const openCommentDialog = (lineNumber: number) => {
      const model = editor.getModel();
      const selection = editor.getSelection();
      let selectedText = "";
      let startLine = lineNumber;
      let endLine = lineNumber;
      if (selection && !selection.isEmpty() && model) {
        selectedText = model.getValueInRange(selection);
        startLine = selection.startLineNumber;
        endLine = selection.endLineNumber;
      } else if (model) {
        selectedText = model.getLineContent(lineNumber) || "";
      }
      pointDialogAt({
        line: startLine,
        side: "right",
        selectedText,
        startLine,
        endLine,
      });
    };

    // A click counts as a "comment on this line" gesture if it lands on the
    // line text/empty area OR on the gutter (line numbers, line decorations,
    // or the glyph-margin comment indicator).
    const isCommentTarget = (target: Monaco.editor.IMouseTarget) => {
      const T = monaco.editor.MouseTargetType;
      return (
        target.type === T.CONTENT_TEXT ||
        target.type === T.CONTENT_EMPTY ||
        target.type === T.GUTTER_GLYPH_MARGIN ||
        target.type === T.GUTTER_LINE_NUMBERS ||
        target.type === T.GUTTER_LINE_DECORATIONS
      );
    };
    const isCommentClickTarget = (e: Monaco.editor.IEditorMouseEvent) => isCommentTarget(e.target);

    // Desktop: on mousedown in comment mode, dismiss any open selection prompt
    // and remember where the press started so we can tell a click from a drag.
    disposables.push(
      editor.onMouseDown((e: Monaco.editor.IEditorMouseEvent) => {
        if (deps.isMobile()) return;
        if (deps.mode() !== "comment") return;
        commentPrompt.value = null;
        // Starting a new selection/click with an empty comment box hides it, so
        // an abandoned empty dialog doesn't linger while you pick a new section.
        if (showCommentDialog.value && !commentText.value.trim()) {
          showCommentDialog.value = null;
        }
        const be = e.event.browserEvent;
        mouseDownPos = { x: be.clientX, y: be.clientY };
      }),
    );

    // Desktop: decide on mouseup. If the user made a text selection, show a
    // floating "Comment" prompt next to it (so the selection stays usable). If
    // it was just a click on a line with no selection, open the comment dialog
    // for that line directly.
    disposables.push(
      editor.onMouseUp((e: Monaco.editor.IEditorMouseEvent) => {
        if (deps.isMobile()) return;
        if (deps.mode() !== "comment") return;
        const model = editor.getModel();
        const selection = editor.getSelection();
        const be = e.event.browserEvent;
        if (selection && !selection.isEmpty() && model) {
          // Show the floating prompt near the mouse-up point.
          const rect = deps.promptHost()?.getBoundingClientRect();
          if (rect) {
            commentPrompt.value = {
              top: be.clientY - rect.top + 8,
              left: Math.max(0, Math.min(be.clientX - rect.left, rect.width - 110)),
              startLine: selection.startLineNumber,
              endLine: selection.endLineNumber,
              selectedText: model.getValueInRange(selection),
            };
          }
          return;
        }
        // No selection: treat as a click to comment on the line, but only if
        // the pointer didn't move (a tiny drag that collapsed to an empty
        // selection shouldn't trigger the dialog).
        if (mouseDownPos) {
          const dx = be.clientX - mouseDownPos.x;
          const dy = be.clientY - mouseDownPos.y;
          mouseDownPos = null;
          if (dx * dx + dy * dy > 16) return;
        }
        if (isCommentClickTarget(e)) {
          const position = e.target.position;
          if (position) openCommentDialog(position.lineNumber);
        }
      }),
    );

    // Mobile: track tap-without-scroll.
    const onTouchStart = (e: TouchEvent) => {
      if (e.touches.length !== 1) {
        touchScrolled = true;
        touchStartPos = null;
        return;
      }
      touchScrolled = false;
      const t = e.touches[0];
      touchStartPos = {
        x: t.clientX,
        y: t.clientY,
        id: t.identifier,
        time: Date.now(),
        scrollTop: editor.getScrollTop(),
        scrollLeft: editor.getScrollLeft(),
      };
    };
    const onTouchMove = (e: TouchEvent) => {
      if (touchScrolled || !touchStartPos) return;
      const t = Array.from(e.touches).find((touch) => touch.identifier === touchStartPos?.id);
      if (!t) {
        touchScrolled = true;
        return;
      }
      const dx = t.clientX - touchStartPos.x;
      const dy = t.clientY - touchStartPos.y;
      if (dx * dx + dy * dy > 100) touchScrolled = true;
    };
    const onTouchEnd = (e: TouchEvent) => {
      const start = touchStartPos;
      touchStartPos = null;
      if (!start || touchScrolled || e.touches.length || !deps.isMobile()) return;
      const t = Array.from(e.changedTouches).find((touch) => touch.identifier === start.id);
      if (!t) return;
      const dx = t.clientX - start.x;
      const dy = t.clientY - start.y;
      if (
        dx * dx + dy * dy > 100 ||
        Date.now() - start.time > 500 ||
        editor.getScrollTop() !== start.scrollTop ||
        editor.getScrollLeft() !== start.scrollLeft
      ) {
        touchScrolled = true;
        return;
      }
      if (deps.mode() !== "comment") return;
      const target = editor.getTargetAtClientPoint(t.clientX, t.clientY);
      if (target && isCommentTarget(target) && target.position) {
        // Suppress the compatibility mouse events, which would open it twice.
        e.preventDefault();
        commentPrompt.value = null;
        openCommentDialog(target.position.lineNumber);
      }
    };
    const onTouchCancel = () => {
      touchStartPos = null;
      touchScrolled = true;
    };
    editorDom.addEventListener("touchstart", onTouchStart, { passive: true });
    editorDom.addEventListener("touchmove", onTouchMove, { passive: true });
    editorDom.addEventListener("touchend", onTouchEnd);
    editorDom.addEventListener("touchcancel", onTouchCancel, { passive: true });

    disposables.push(
      editor.onMouseUp((e: Monaco.editor.IEditorMouseEvent) => {
        if (!deps.isMobile()) return;
        if (deps.mode() !== "comment") return;
        const source = e.event.browserEvent as MouseEvent & {
          sourceCapabilities?: { firesTouchEvents: boolean };
        };
        if (touchStartPos || (touchScrolled && source.sourceCapabilities?.firesTouchEvents)) return;
        if (isCommentClickTarget(e)) {
          const position = e.target.position;
          if (position) openCommentDialog(position.lineNumber);
        }
      }),
    );

    // Hover highlighting with comment indicator (comment mode only).
    let lastHoveredLine = -1;
    disposables.push(
      editor.onMouseMove((e: Monaco.editor.IEditorMouseEvent) => {
        if (deps.mode() !== "comment") {
          if (hoverDecorations.length > 0) {
            hoverDecorations = editor.deltaDecorations(hoverDecorations, []);
          }
          return;
        }
        // Mark only lines a click would comment on: view zones (callouts,
        // inline deleted lines) report a nearby line but don't count.
        const lineNumber = isCommentClickTarget(e) ? (e.target.position?.lineNumber ?? -1) : -1;
        if (lineNumber === lastHoveredLine) return;
        lastHoveredLine = lineNumber;
        if (lineNumber > 0) {
          hoverDecorations = editor.deltaDecorations(hoverDecorations, [
            {
              range: new monaco.Range(lineNumber, 1, lineNumber, 1),
              options: {
                isWholeLine: true,
                className: "diff-viewer-line-hover",
                glyphMarginClassName: "diff-viewer-comment-glyph",
              },
            },
          ]);
        } else {
          hoverDecorations = editor.deltaDecorations(hoverDecorations, []);
        }
      }),
    );

    disposables.push(
      editor.onMouseLeave(() => {
        lastHoveredLine = -1;
        hoverDecorations = editor.deltaDecorations(hoverDecorations, []);
      }),
    );

    return () => {
      editorDom.removeEventListener("touchstart", onTouchStart);
      editorDom.removeEventListener("touchmove", onTouchMove);
      editorDom.removeEventListener("touchend", onTouchEnd);
      editorDom.removeEventListener("touchcancel", onTouchCancel);
      for (const d of disposables) d.dispose();
    };
  }

  function handleAddComment() {
    if (!showCommentDialog.value || !commentText.value.trim()) return;
    const fileRef = deps.fileRef();
    if (!fileRef) return;
    const line = showCommentDialog.value.line;
    const codeSnippet = showCommentDialog.value.selectedText?.split("\n")[0]?.trim() || "";
    const truncatedCode = truncateWithEllipsis(codeSnippet, 60);
    const highlight = showCommentDialog.value.highlight;
    const quote = highlight ? `\n> ${highlightQuote(highlight)}` : "";
    const commentBlock = `> ${fileRef}:${line}: ${truncatedCode}${quote}\n${commentText.value}\n\n`;
    deps.onSubmit(commentBlock);
    showCommentDialog.value = null;
    commentText.value = "";
  }

  // Open the comment dialog from the floating selection prompt, preserving the
  // lines and text that were selected when the prompt appeared.
  function openCommentFromPrompt() {
    const p = commentPrompt.value;
    if (!p) return;
    pointDialogAt({
      line: p.startLine,
      side: "right",
      selectedText: p.selectedText,
      startLine: p.startLine,
      endLine: p.endLine,
    });
    commentPrompt.value = null;
  }

  // Open the dialog on a commit highlight's line, to answer the highlight.
  function commentOnHighlight(line: number, lineText: string, highlight: string) {
    commentPrompt.value = null;
    pointDialogAt({
      line,
      side: "right",
      selectedText: lineText,
      startLine: line,
      endLine: line,
      highlight,
    });
  }

  function clearPrompt() {
    commentPrompt.value = null;
  }

  function reset() {
    showCommentDialog.value = null;
    commentPrompt.value = null;
    commentText.value = "";
  }

  return {
    showCommentDialog,
    commentDialogOpens,
    commentPrompt,
    commentText,
    attach,
    handleAddComment,
    openCommentFromPrompt,
    commentOnHighlight,
    clearPrompt,
    reset,
  };
}
