import { ref } from "vue";
import { SLASH_COMMANDS } from "../../utils/slashCommands";

const DEFAULT_PREVIEW_PORT = 8000;
const MIN_PREVIEW_PORT = 3000;
const MAX_PREVIEW_PORT = 9999;
const DEFAULT_PREVIEW_PATH = "/";

export interface PreviewEndpoint {
  port: number;
  path: string;
}

export type PreviewCommand = { action: "open"; port?: number; path?: string } | { action: "close" };

const USAGE = "Usage: /preview [port [path]|off]";

export function previewURL(port: number, path = DEFAULT_PREVIEW_PATH): string {
  return `/__preview/${port}${path}`;
}

export function previewPortError(port: number): string | null {
  if (!Number.isInteger(port) || port < MIN_PREVIEW_PORT || port > MAX_PREVIEW_PORT) {
    return `Preview ports must be between ${MIN_PREVIEW_PORT} and ${MAX_PREVIEW_PORT}`;
  }
  return null;
}

function normalizePreviewPath(path: string): string | null {
  const hasControl = [...path].some((character) => {
    const code = character.charCodeAt(0);
    return code < 32 || code === 127;
  });
  if (
    !path ||
    /\s/.test(path) ||
    hasControl ||
    path.includes("\\") ||
    path.startsWith("//") ||
    /^[a-z][a-z\d+.-]*:/i.test(path)
  ) {
    return null;
  }
  return path.startsWith("/") ? path : `/${path}`;
}

function checkedPath(path: string): string {
  const normalized = normalizePreviewPath(path);
  if (!normalized)
    throw new Error("Preview paths must be relative paths without whitespace or backslashes");
  return normalized;
}

function storedPort(): number {
  if (typeof window === "undefined") return DEFAULT_PREVIEW_PORT;
  const raw = localStorage.getItem("shelley-live-preview-port") ?? "";
  const port = Number(raw);
  return /^\d{4}$/.test(raw) && !previewPortError(port) ? port : DEFAULT_PREVIEW_PORT;
}

function storedPath(): string {
  if (typeof window === "undefined") return DEFAULT_PREVIEW_PATH;
  return (
    normalizePreviewPath(localStorage.getItem("shelley-live-preview-path") ?? "") ??
    DEFAULT_PREVIEW_PATH
  );
}

export const livePreviewOpen = ref(false);
export const livePreviewPort = ref(storedPort());
export const livePreviewPath = ref(storedPath());
export const livePreviewRevision = ref(0);

export function isPreviewCommand(message: string): boolean {
  return message.trim().split(/\s/, 1)[0] === SLASH_COMMANDS.PREVIEW.command;
}

export function parsePreviewCommand(message: string): PreviewCommand {
  const match = message.trim().match(/^\/preview(?:\s+(\S+)(?:\s+(\S+))?)?$/);
  if (!match) throw new Error(USAGE);
  const argument = match[1]?.toLowerCase();
  const path = match[2];
  if (!argument) return { action: "open" };
  if (argument === "off" && !path) return { action: "close" };
  if (!/^\d{4}$/.test(argument)) throw new Error(USAGE);
  const port = Number(argument);
  const portError = previewPortError(port);
  if (portError) throw new Error(portError);
  return { action: "open", port, path: checkedPath(path ?? DEFAULT_PREVIEW_PATH) };
}

function endpoint(port: number, path: string | undefined): PreviewEndpoint | null {
  if (previewPortError(port)) return null;
  const normalizedPath = normalizePreviewPath(path ?? DEFAULT_PREVIEW_PATH);
  return normalizedPath ? { port, path: normalizedPath } : null;
}

export function previewEndpointFromText(text: string): PreviewEndpoint | null {
  const candidates: Array<{ index: number; endpoint: PreviewEndpoint }> = [];
  const add = (index: number, port: number, path?: string) => {
    const found = endpoint(port, path);
    if (found) candidates.push({ index, endpoint: found });
  };

  for (const match of text.matchAll(/(?:^|[\s`"'(])\/preview\s+(\d{4})\b(?:\s+([^\s`"'<>]+))?/g)) {
    add((match.index ?? -1) + match[0].indexOf("/preview"), Number(match[1]), match[2]);
  }
  for (const match of text.matchAll(/https?:\/\/[^\s`"'<>]+/gi)) {
    try {
      const url = new URL(match[0]);
      if (url.port)
        add(match.index ?? -1, Number(url.port), `${url.pathname}${url.search}${url.hash}`);
    } catch {
      // Ignore malformed URLs.
    }
  }
  for (const match of text.matchAll(/\blocalhost:(\d{4})([^\s`"'<>]*)/gi)) {
    add(match.index ?? -1, Number(match[1]), match[2] || undefined);
  }
  for (const match of text.matchAll(/\bport\s*(?:=|:)?\s*(\d{4})\b/gi)) {
    add(match.index ?? -1, Number(match[1]));
  }
  for (const match of text.matchAll(/(?:^|\s)(?:--?port|-p)\s*(?:=)?\s*(\d{4})\b/gi)) {
    add(match.index ?? -1, Number(match[1]));
  }

  return (
    candidates.reduce<{ index: number; endpoint: PreviewEndpoint } | null>(
      (latest, candidate) => (!latest || candidate.index > latest.index ? candidate : latest),
      null,
    )?.endpoint ?? null
  );
}

export function mostRecentPreviewEndpoint(texts: string[]): PreviewEndpoint | null {
  for (let index = texts.length - 1; index >= 0; index--) {
    const found = previewEndpointFromText(texts[index]);
    if (found) return found;
  }
  return null;
}

export function openLivePreview(port = livePreviewPort.value, path = livePreviewPath.value) {
  const portError = previewPortError(port);
  if (portError) throw new Error(portError);
  const normalizedPath = checkedPath(path);
  livePreviewPort.value = port;
  livePreviewPath.value = normalizedPath;
  livePreviewOpen.value = true;
  livePreviewRevision.value++;
  if (typeof window !== "undefined") {
    localStorage.setItem("shelley-live-preview-port", String(port));
    localStorage.setItem("shelley-live-preview-path", normalizedPath);
  }
}

export function syncLivePreviewPath(frameURL: string) {
  const match = new URL(frameURL).pathname.match(/^\/__preview\/(\d{4})(\/.*)$/);
  if (!match || Number(match[1]) !== livePreviewPort.value) return;
  livePreviewPath.value = match[2] + new URL(frameURL).search;
}

export function closeLivePreview() {
  livePreviewOpen.value = false;
}

export function refreshLivePreview() {
  if (!livePreviewOpen.value) return;
  livePreviewRevision.value++;
}
