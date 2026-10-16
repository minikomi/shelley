import type { LLMMessage, Message, QueuedMessage } from "../types";

export interface LiveTranscriptTurn {
  user: boolean;
  text: string;
}

interface Fragment extends LiveTranscriptTurn {
  through: number;
  startMs: number;
  endMs: number;
}

const thoughtGapMs = 1500;

export interface LiveTranscriptRecord {
  session_id: string;
  message: string;
  fragments: Array<{
    n: number;
    user: boolean;
    text: string;
    start_ms: number;
    end_ms: number;
  }>;
}

export function readLiveTranscript(message: Message): LiveTranscriptRecord | undefined {
  if (message.type !== "livetranscript") return;
  return JSON.parse(message.user_data!) as LiveTranscriptRecord;
}

const voiceExchange = "\n\nVoice exchange (for context only):\n";

function transcriptExtensions(messages: readonly Message[]) {
  const extensions = new Map<string, LiveTranscriptRecord["fragments"]>();
  const seen = new Set<string>();
  for (const message of messages) {
    const record = readLiveTranscript(message);
    if (!record) continue;
    let fragments = extensions.get(record.message);
    if (!fragments) extensions.set(record.message, (fragments = []));
    for (const fragment of record.fragments) {
      const key = `${record.session_id}\0${fragment.n}`;
      if (seen.has(key)) continue;
      seen.add(key);
      fragments.push(fragment);
    }
  }
  return extensions;
}

function extendMessage(
  llm: LLMMessage,
  extensions: ReturnType<typeof transcriptExtensions>,
): LLMMessage {
  const text = llm.Content.filter((content) => content.Type === 2)
    .map((content) => content.Text ?? "")
    .join("\n");
  const fragments = extensions.get(text);
  if (!fragments?.length) return llm;
  let extended = text;
  let speaker: string | undefined;
  if (!text.includes(voiceExchange)) extended += voiceExchange;
  else {
    for (const match of text
      .slice(text.indexOf(voiceExchange))
      .matchAll(/(?:^|\n)(USER|LIVE): /g)) {
      speaker = match[1];
    }
  }
  for (const fragment of fragments) {
    if (!fragment.text) continue;
    const label = fragment.user ? "USER" : "LIVE";
    if (speaker === label) extended += fragment.text;
    else {
      extended += `${extended.endsWith("\n") ? "" : "\n"}${label}: ${fragment.text.trimStart()}`;
      speaker = label;
    }
  }
  const lastText = llm.Content.map((content) => content.Type).lastIndexOf(2);
  return {
    ...llm,
    Content: llm.Content.map((content, index) =>
      index === lastText
        ? { ...content, Text: (content.Text ?? "") + extended.slice(text.length) }
        : content,
    ),
  };
}

export function projectLiveTranscripts(messages: Message[]): Message[] {
  const extensions = transcriptExtensions(messages);
  return messages
    .filter((message) => message.type !== "livetranscript")
    .map((message) => {
      if (message.type !== "user" || !message.llm_data || extensions.size === 0) return message;
      const llm = JSON.parse(message.llm_data) as LLMMessage;
      const projected = extendMessage(llm, extensions);
      return projected === llm ? message : { ...message, llm_data: JSON.stringify(projected) };
    });
}

export function projectLiveQueuedMessages(
  queued: QueuedMessage[],
  messages: Message[],
): QueuedMessage[] {
  const extensions = transcriptExtensions(messages);
  if (!extensions.size) return queued;
  return queued.map((message) => {
    if (!message.llm) return message;
    const projected = extendMessage(message.llm, extensions);
    return projected === message.llm ? message : { ...message, llm: projected };
  });
}

export class LiveTranscript {
  private count = 0;
  private consumed = 0;
  private fragments: Fragment[] = [];
  private handoffs = new Map<number, { message: string; fragments?: number[] }>();

  append(user: boolean, text: string, startMs: number, endMs: number) {
    this.fragments.push({ through: ++this.count, user, text, startMs, endMs });
  }

  handoff(through: number, message: string, fragments?: number[]) {
    if (through > this.consumed) this.handoffs.set(through, { message, fragments });
  }

  consume(ids: readonly number[]) {
    const consumed = new Set(ids);
    this.fragments = this.fragments.filter((fragment) => !consumed.has(fragment.through));
  }

  reconcile(savedMessages: readonly string[]) {
    const saved = new Set(savedMessages);
    for (const [through, handoff] of this.handoffs) {
      if (!saved.has(handoff.message)) continue;
      if (handoff.fragments) this.consume(handoff.fragments);
      else this.consumed = Math.max(this.consumed, through);
      this.handoffs.delete(through);
    }
    this.fragments = this.fragments.filter((fragment) => fragment.through > this.consumed);
    for (const through of this.handoffs.keys()) {
      if (through <= this.consumed) this.handoffs.delete(through);
    }
  }

  get turns(): LiveTranscriptTurn[] {
    const turns: Array<LiveTranscriptTurn & { endMs: number }> = [];
    const latest = new Map<boolean, (typeof turns)[number]>();
    const fragments = [...this.fragments].sort(
      (a, b) => a.startMs - b.startMs || a.through - b.through,
    );
    for (const fragment of fragments) {
      const last = latest.get(fragment.user);
      if (last && fragment.startMs - last.endMs <= thoughtGapMs) {
        last.text += fragment.text;
        last.endMs = Math.max(last.endMs, fragment.endMs);
      } else {
        const turn = { user: fragment.user, text: fragment.text, endMs: fragment.endMs };
        turns.push(turn);
        latest.set(fragment.user, turn);
      }
    }
    return turns
      .filter((turn) => turn.text.trim() !== "")
      .map(({ user, text }) => ({ user, text }));
  }
}
