export type TaskGraphState = "active" | "complete" | "failed" | "cancelled";

export type TaskState =
  | "pending"
  | "ready"
  | "starting"
  | "running"
  | "complete"
  | "failed"
  | "cancelled";

export interface TaskGraphTask {
  id: string;
  title: string;
  owner: "parent" | "subagent";
  state: TaskState;
  depends_on?: string[];
  slug?: string;
  model?: string;
  child_conversation_id?: string;
  result?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
}

export interface TaskGraphSnapshot {
  id: string;
  parent_conversation_id: string;
  title: string;
  state: TaskGraphState;
  created_at: string;
  updated_at: string;
  tasks: TaskGraphTask[];
}

export function taskGraphSnapshot(value: unknown): TaskGraphSnapshot | null {
  if (!value || typeof value !== "object") return null;
  const graph = value as Partial<TaskGraphSnapshot>;
  if (
    typeof graph.id !== "string" ||
    typeof graph.title !== "string" ||
    !Array.isArray(graph.tasks)
  ) {
    return null;
  }
  return graph as TaskGraphSnapshot;
}
