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

await run("getTaskGraphs requests the conversation graphs", async () => {
  let requestedURL = "";
  await withFetch(
    (async (input) => {
      requestedURL = String(input);
      return Response.json([
        {
          id: "graph-1",
          parent_conversation_id: "parent/id",
          title: "Build feature",
          state: "active",
          created_at: "2026-09-20T00:00:00Z",
          updated_at: "2026-09-20T00:00:00Z",
          tasks: [],
        },
      ]);
    }) as typeof globalThis.fetch,
    async () => {
      const graphs = await api.getTaskGraphs("parent/id", { active: true });
      assert(
        graphs.length === 1 && graphs[0].id === "graph-1",
        `graphs = ${JSON.stringify(graphs)}`,
      );
    },
  );
  assert(
    requestedURL === "/api/conversation/parent%2Fid/task-graphs?active=1",
    `url = ${requestedURL}`,
  );
});

await run("getTaskGraphs requests one graph by id", async () => {
  let requestedURL = "";
  await withFetch(
    (async (input) => {
      requestedURL = String(input);
      return Response.json([]);
    }) as typeof globalThis.fetch,
    async () => {
      await api.getTaskGraphs("parent", { graphId: "graph/id" });
    },
  );
  assert(
    requestedURL === "/api/conversation/parent/task-graphs?graph_id=graph%2Fid",
    `url = ${requestedURL}`,
  );
});

await run("getTaskGraphs propagates server failures", async () => {
  await withFetch(
    (async () => new Response("boom", { status: 500 })) as typeof globalThis.fetch,
    async () => {
      try {
        await api.getTaskGraphs("parent");
        throw new Error("expected task graph request to reject");
      } catch (error) {
        assert(error instanceof ApiError, `error = ${String(error)}`);
        assert(error.status === 500, `status = ${error.status}`);
      }
    },
  );
});
