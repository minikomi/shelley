import { ApiError, api } from "./api";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

async function withFetch(fake: typeof globalThis.fetch, fn: () => Promise<void>): Promise<void> {
  const original = globalThis.fetch;
  globalThis.fetch = fake;
  try {
    await fn();
  } finally {
    globalThis.fetch = original;
  }
}

async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`✓ ${name}`);
  } catch (error) {
    console.error(`✗ ${name}`);
    throw error;
  }
}

await run("getLatestTaskGraph requests the conversation graph", async () => {
  let requestedURL = "";
  await withFetch(
    (async (input) => {
      requestedURL = String(input);
      return Response.json({
        id: "graph-1",
        parent_conversation_id: "parent/id",
        title: "Build feature",
        state: "active",
        created_at: "2026-09-20T00:00:00Z",
        updated_at: "2026-09-20T00:00:00Z",
        tasks: [],
      });
    }) as typeof globalThis.fetch,
    async () => {
      const graph = await api.getLatestTaskGraph("parent/id");
      assert(graph?.id === "graph-1", `graph = ${JSON.stringify(graph)}`);
    },
  );
  assert(requestedURL === "/api/conversation/parent%2Fid/task-graph", `url = ${requestedURL}`);
});

await run("getLatestTaskGraph returns null when no graph exists", async () => {
  await withFetch(
    (async () => new Response(null, { status: 404 })) as typeof globalThis.fetch,
    async () => {
      assert((await api.getLatestTaskGraph("parent")) === null, "expected no graph");
    },
  );
});

await run("getLatestTaskGraph propagates server failures", async () => {
  await withFetch(
    (async () => new Response("boom", { status: 500 })) as typeof globalThis.fetch,
    async () => {
      try {
        await api.getLatestTaskGraph("parent");
        throw new Error("expected task graph request to reject");
      } catch (error) {
        assert(error instanceof ApiError, `error = ${String(error)}`);
        assert(error.status === 500, `status = ${error.status}`);
      }
    },
  );
});
