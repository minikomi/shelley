export type RecordingMode = "microphone" | "screen";

// A recording is bound at start, never to the conversation selected at stop.
export interface RecordingDestination {
  conversationId: string;
  complete: (path: string, context: string) => Promise<void>;
  returnTo: () => Promise<void>;
}
