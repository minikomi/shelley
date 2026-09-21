import type { LLMMessage, Message } from "../types";

const parsedMessages = new WeakMap<Message, LLMMessage | null>();

export function parseLLMMessage(message: Message): LLMMessage | null {
  const cached = parsedMessages.get(message);
  if (cached !== undefined) return cached;
  let parsed: LLMMessage | null = null;
  if (message.llm_data) {
    try {
      parsed =
        typeof message.llm_data === "string"
          ? (JSON.parse(message.llm_data) as LLMMessage)
          : (message.llm_data as LLMMessage);
    } catch {
      parsed = null;
    }
  }
  parsedMessages.set(message, parsed);
  return parsed;
}
