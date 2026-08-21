export interface SlashCommand {
  command: `/${string}`;
  description: string;
  takesArgs: boolean;
}

export const SLASH_COMMANDS = {
  FORK: {
    command: "/fork",
    description: "forks this conversation",
    takesArgs: false,
  },
  DIFF: {
    command: "/diff",
    description: "opens the diff viewer",
    takesArgs: false,
  },
  SHELL: {
    command: "/shell",
    description: "runs in shell (! alias)",
    takesArgs: true,
  },
  COMPACT: {
    command: "/compact",
    description: "compacts this conversation",
    takesArgs: true,
  },
  CHECKPOINT: {
    // Same compaction mechanics as /compact, but the summary uses the
    // topic-based checkpoint format (durable topics with context, decisions,
    // rationale, anchors) instead of the task-report format.
    command: "/checkpoint",
    description: "compacts with a topic-based checkpoint summary",
    takesArgs: true,
  },
  DISTILL: {
    // Legacy alias for /compact, kept for compatibility. Compacts too.
    command: "/distill",
    description: "compacts this conversation (alias for /compact)",
    takesArgs: true,
  },
  CLEAR: {
    command: "/clear",
    description: "clears context, keeping this conversation",
    takesArgs: false,
  },
  NEW: {
    command: "/new",
    description: "starts a new conversation",
    takesArgs: true,
  },
  ARCHIVE: {
    command: "/archive",
    description: "archives this conversation",
    takesArgs: false,
  },
  RENAME: {
    command: "/rename",
    description: "renames this conversation",
    takesArgs: true,
  },
} as const satisfies Record<string, SlashCommand>;
