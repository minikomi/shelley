import type {
  ConversationListPatchEvent,
  ConversationListPatchOp,
  ConversationWithState,
} from "../types";

function decodePointer(path: string): string[] {
  if (path === "") return [];
  if (!path.startsWith("/")) {
    throw new Error(`invalid JSON pointer: ${path}`);
  }
  return path
    .slice(1)
    .split("/")
    .map((part) => part.replace(/~1/g, "/").replace(/~0/g, "~"));
}

function cloneValue<T>(value: T): T {
  return value === undefined ? value : JSON.parse(JSON.stringify(value));
}

function getAt(doc: unknown, path: string): unknown {
  let cur = doc;
  for (const part of decodePointer(path)) {
    if (Array.isArray(cur)) {
      const idx = Number(part);
      if (!Number.isInteger(idx) || idx < 0 || idx >= cur.length) {
        throw new Error(`bad array index in patch path: ${path} (len=${cur.length})`);
      }
      cur = cur[idx];
    } else if (cur !== null && typeof cur === "object") {
      cur = (cur as Record<string, unknown>)[part];
    } else {
      throw new Error(`cannot traverse patch path: ${path}`);
    }
  }
  return cur;
}

function setAt(doc: unknown, path: string, value: unknown, mustExist: boolean): unknown {
  if (path === "") return cloneValue(value);
  const parts = decodePointer(path);
  const update = (node: unknown, depth: number): unknown => {
    const key = parts[depth];
    const final = depth === parts.length - 1;
    if (Array.isArray(node)) {
      const idx = Number(key);
      const max = final && !mustExist ? node.length : node.length - 1;
      if (!Number.isInteger(idx) || idx < 0 || idx > max) {
        throw new Error(`bad array index in patch path: ${path} (len=${node.length})`);
      }
      const next = node.slice();
      if (!final) {
        next[idx] = update(node[idx], depth + 1);
      } else if (idx === node.length) {
        next.push(cloneValue(value));
      } else if (mustExist) {
        next[idx] = cloneValue(value);
      } else {
        next.splice(idx, 0, cloneValue(value));
      }
      return next;
    }
    if (node !== null && typeof node === "object") {
      const current = node as Record<string, unknown>;
      if (!final && !(key in current)) {
        throw new Error(`cannot traverse patch path: ${path}`);
      }
      if (final && mustExist && !(key in current)) {
        throw new Error(`missing object key in patch path: ${path}`);
      }
      return {
        ...current,
        [key]: final ? cloneValue(value) : update(current[key], depth + 1),
      };
    }
    throw new Error(`cannot traverse patch path: ${path}`);
  };
  return update(doc, 0);
}

function removeAt(doc: unknown, path: string): unknown {
  if (path === "") throw new Error("cannot remove document root");
  const parts = decodePointer(path);
  const update = (node: unknown, depth: number): unknown => {
    const key = parts[depth];
    const final = depth === parts.length - 1;
    if (Array.isArray(node)) {
      const idx = Number(key);
      if (!Number.isInteger(idx) || idx < 0 || idx >= node.length) {
        throw new Error(`bad array index in patch path: ${path} (len=${node.length})`);
      }
      const next = node.slice();
      if (final) {
        next.splice(idx, 1);
      } else {
        next[idx] = update(node[idx], depth + 1);
      }
      return next;
    }
    if (node !== null && typeof node === "object") {
      const current = node as Record<string, unknown>;
      if (!(key in current)) {
        throw new Error(`cannot remove patch path: ${path}`);
      }
      const next = { ...current };
      if (final) {
        delete next[key];
      } else {
        next[key] = update(current[key], depth + 1);
      }
      return next;
    }
    throw new Error(`cannot remove patch path: ${path}`);
  };
  return update(doc, 0);
}

function validateOp(op: ConversationListPatchOp): void {
  if (typeof op.path !== "string") {
    throw new Error(`patch op ${op.op} is missing path`);
  }
  if ((op.op === "add" || op.op === "replace") && !("value" in op)) {
    throw new Error(`patch op ${op.op} is missing value`);
  }
  if (op.op === "move" && typeof op.from !== "string") {
    throw new Error("move patch is missing from");
  }
}

export function applyConversationListPatch(
  state: ConversationWithState[],
  patch: ConversationListPatchOp[],
): ConversationWithState[] {
  let doc: unknown = state;
  for (const op of patch) {
    validateOp(op);
    switch (op.op) {
      case "replace":
        doc = setAt(doc, op.path, op.value, op.path !== "");
        break;
      case "add":
        doc = setAt(doc, op.path, op.value, false);
        break;
      case "remove":
        doc = removeAt(doc, op.path);
        break;
      case "move": {
        const value = cloneValue(getAt(doc, op.from!));
        doc = removeAt(doc, op.from!);
        doc = setAt(doc, op.path, value, false);
        break;
      }
      default: {
        const exhaustive: never = op.op;
        throw new Error(`unsupported patch op: ${exhaustive}`);
      }
    }
  }
  if (!Array.isArray(doc)) {
    throw new Error("conversation list patch did not produce an array");
  }
  return doc as ConversationWithState[];
}

// ConversationListState couples the materialized conversation list with the
// hash it was produced under. The two MUST advance together: the patch stream
// is a strict old_hash->new_hash chain, so applying a patch requires that the
// `list` we apply it to is exactly the one `hash` describes. Keeping them in a
// single value (rather than two independent refs) makes it impossible to
// advance one without the other — the desync that let a patch built against
// new state land on a stale list, corrupting rows (e.g. a /N/preview replace
// landing on the wrong conversation).
export interface ConversationListState {
  list: ConversationWithState[];
  hash: string | null;
}

export type ConversationListReduceResult =
  | { ok: true; state: ConversationListState; removedIds: string[] }
  | { ok: false; reason: "hash-mismatch"; eventOldHash: string | null }
  | { ok: false; reason: "apply-failed"; error: unknown };

// reduceConversationListPatch applies a single patch event to `state`,
// returning the next coupled {list, hash} plus the ids dropped from the list.
// It refuses (ok:false) when the event doesn't anchor to the current hash or
// when the patch can't be applied, so the caller can recover via reconnect
// WITHOUT having mutated either half of the state — preserving the lock
// between list and hash.
export function reduceConversationListPatch(
  state: ConversationListState,
  event: ConversationListPatchEvent,
): ConversationListReduceResult {
  if (!event.reset && (event.old_hash ?? null) !== state.hash) {
    return { ok: false, reason: "hash-mismatch", eventOldHash: event.old_hash ?? null };
  }
  let nextList: ConversationWithState[];
  try {
    nextList = applyConversationListPatch(state.list, event.patch);
  } catch (error) {
    return { ok: false, reason: "apply-failed", error };
  }
  const nextIds = new Set(nextList.map((conv) => conv.conversation_id));
  const removedIds: string[] = [];
  for (const conv of state.list) {
    if (!nextIds.has(conv.conversation_id)) removedIds.push(conv.conversation_id);
  }
  return { ok: true, state: { list: nextList, hash: event.new_hash }, removedIds };
}
