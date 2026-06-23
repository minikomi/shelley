import React, { useMemo } from "react";
import { renderInlineText } from "../utils/inlineText";
import { renderMarkdownToSafeHTML } from "../utils/markdownRender";
import type { Citation } from "../utils/coalesceContent";

interface CitedTextProps {
  // Raw concatenation of the merged text run (used when markdown is off).
  text: string;
  // Same text with inline <sup> citation markers injected (used for markdown).
  markdownText: string;
  // De-duplicated, numbered sources referenced by this run.
  citations: Citation[];
  renderMarkdown: boolean;
  messageId?: string;
}

// CitedText renders one coalesced run of adjacent text blocks. In markdown
// mode it renders the marker-augmented text and, when there are citations,
// appends a compact numbered "Sources" list. In raw mode it renders the plain
// concatenation through the inline-text formatter (markers are omitted).
function CitedText({ text, markdownText, citations, renderMarkdown, messageId }: CitedTextProps) {
  const html = useMemo(
    () => (renderMarkdown ? renderMarkdownToSafeHTML(markdownText, messageId) : ""),
    [renderMarkdown, markdownText, messageId],
  );
  if (!renderMarkdown) {
    return <div className="whitespace-pre-wrap break-words">{renderInlineText(text)}</div>;
  }
  return (
    <>
      <div className="markdown-content break-words" dangerouslySetInnerHTML={{ __html: html }} />
      {citations.length > 0 && (
        <ol className="citation-sources">
          {citations.map((c, i) => (
            <li key={i} className="citation-source">
              <span className="citation-source-num">{c.num}</span>
              <a
                href={c.url}
                target="_blank"
                rel="noopener noreferrer"
                className="citation-source-link"
                title={c.url}
              >
                {c.title || c.url}
              </a>
            </li>
          ))}
        </ol>
      )}
    </>
  );
}

export default CitedText;
