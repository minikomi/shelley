export type RecordingMode = "microphone" | "screen";

export type RecordingResult = { kind: "file"; path: string } | { kind: "transcript"; text: string };

// A recording is bound at start, never to the conversation selected at stop.
export interface RecordingDestination {
  conversationId: string;
  complete: (result: RecordingResult, context: string) => Promise<void>;
  // stillWanted is checked after the lookup, before navigating.
  returnTo: (stillWanted: () => boolean) => Promise<void>;
}

export interface RecordingPreparation {
  resolve: () => Promise<RecordingDestination>;
  release: () => void;
}
