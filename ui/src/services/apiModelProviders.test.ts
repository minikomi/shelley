// Provider management client behaviour: error bodies and refresh parsing.
// Run with: tsx src/services/apiModelProviders.test.ts
import { api, ApiError } from "./api";

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

async function failure(fn: () => Promise<unknown>): Promise<ApiError> {
  try {
    await fn();
  } catch (error) {
    assert(error instanceof ApiError, `expected ApiError, got ${String(error)}`);
    return error;
  }
  throw new Error("Assertion failed: expected a rejection");
}

const input = { display_name: "", provider_type: "openai" as const, endpoint: "x", api_key: "k" };

await run("provider error JSON shows only its message", async () => {
  await withFetch(
    (async () =>
      Response.json(
        { error: "a provider with this endpoint and API key is already saved" },
        { status: 409 },
      )) as typeof globalThis.fetch,
    async () => {
      const error = await failure(() => api.createModelProvider(input));
      assert(
        error.message ===
          "Failed to add provider connection: a provider with this endpoint and API key is already saved",
        error.message,
      );
      assert(error.status === 409, `status = ${error.status}`);
    },
  );
});

await run("HTML error pages fall back to the status text", async () => {
  await withFetch(
    (async () =>
      new Response("<html><body>proxy</body></html>", {
        status: 502,
        statusText: "Bad Gateway",
        headers: { "content-type": "text/html" },
      })) as typeof globalThis.fetch,
    async () => {
      const error = await failure(() => api.refreshModelProvider("p"));
      assert(error.message === "Failed to refresh provider connection: Bad Gateway", error.message);
    },
  );
});

await run("legacy refreshModels keeps the bare array and raw error text", async () => {
  await withFetch(
    (async () => Response.json([{ id: "m", ready: true }])) as typeof globalThis.fetch,
    async () => {
      const models = await api.refreshModels();
      assert(Array.isArray(models) && models.length === 1 && models[0].id === "m", "bare array");
    },
  );
  await withFetch(
    (async () =>
      new Response('{"error":"boom"}', {
        status: 500,
        headers: { "content-type": "application/json" },
      })) as typeof globalThis.fetch,
    async () => {
      const error = await failure(() => api.refreshModels());
      assert(error.message === 'Failed to refresh models: {"error":"boom"}', error.message);
    },
  );
});

await run("generic errors keep raw JSON and HTML bodies", async () => {
  await withFetch(
    (async () =>
      new Response("<html>bad gateway</html>", {
        status: 502,
        statusText: "Bad Gateway",
        headers: { "content-type": "text/html" },
      })) as typeof globalThis.fetch,
    async () => {
      const error = await failure(() => api.getModels());
      assert(error.message === "Failed to get models: <html>bad gateway</html>", error.message);
    },
  );
});

await run(
  "refreshModelProviders posts to the provider endpoint and returns Connection[]",
  async () => {
    const calls: string[] = [];
    await withFetch(
      (async (url: RequestInfo | URL, init?: RequestInit) => {
        calls.push(`${init?.method} ${String(url)}`);
        return Response.json([]);
      }) as typeof globalThis.fetch,
      async () => {
        const sources = await api.refreshModelProviders();
        assert(Array.isArray(sources) && sources.length === 0, "empty result is benign");
        assert(
          calls.length === 1 &&
            calls[0].startsWith("POST ") &&
            calls[0].endsWith("/model-providers/refresh"),
          calls.join(),
        );
      },
    );
    await withFetch(
      (async () => Response.json({ sources: [] })) as typeof globalThis.fetch,
      async () => {
        const error = await failure(() => api.refreshModelProviders());
        assert(error.message === "Invalid provider refresh response", error.message);
      },
    );
  },
);

function connection(id: string, state: "ready" | "error", error?: string) {
  return {
    provider_id: id,
    provider_type: "openai",
    endpoint: "x",
    display_name: id,
    has_api_key: true,
    created_at: "",
    updated_at: "",
    state,
    model_count: 1,
    error,
  };
}

// Fake backend keyed by "METHOD path-suffix".
function backend(routes: Record<string, () => Response>) {
  const calls: string[] = [];
  const fake = (async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url).replace(/^.*\/api/, "");
    const key = `${init?.method ?? "GET"} ${path}`;
    calls.push(key);
    const route = routes[key];
    if (!route) throw new Error(`unexpected request ${key}`);
    return route();
  }) as typeof globalThis.fetch;
  return { fake, calls };
}

await run("catalog refresh: zero connections is the legacy refresh plus final models", async () => {
  const { fake, calls } = backend({
    "POST /models/refresh": () => Response.json([{ id: "a" }]),
    "POST /model-providers/refresh": () => Response.json([]),
    "GET /models": () => Response.json([{ id: "a" }]),
  });
  await withFetch(fake, async () => {
    const result = await api.refreshModelCatalog();
    assert(result.models.length === 1 && result.sources.length === 0, "shape");
    assert(result.error === undefined, `error = ${result.error}`);
    assert(calls[calls.length - 1] === "GET /models", "final models are read last");
  });
});

await run(
  "catalog refresh: a failed provider source keeps its state and reports an error",
  async () => {
    const sources = [connection("ok", "ready"), connection("bad", "error", "401 unauthorized")];
    const { fake } = backend({
      "POST /models/refresh": () => Response.json([{ id: "a" }]),
      "POST /model-providers/refresh": () => Response.json(sources),
      "GET /models": () => Response.json([{ id: "a" }, { id: "provider:ok:m" }]),
    });
    await withFetch(fake, async () => {
      const result = await api.refreshModelCatalog();
      assert(result.models.length === 2, "models from final GET");
      assert(result.sources.length === 2 && result.sources[1].state === "error", "state kept");
      assert(result.error === "bad: 401 unauthorized", `error = ${result.error}`);
    });
  },
);

await run("catalog refresh: legacy failure does not prevent saved refresh", async () => {
  const { fake, calls } = backend({
    "POST /models/refresh": () => new Response("legacy down", { status: 502 }),
    "POST /model-providers/refresh": () => Response.json([connection("ok", "ready")]),
  });
  await withFetch(fake, async () => {
    const error = await failure(() => api.refreshModelCatalog());
    assert(error.message === "Failed to refresh models: legacy down", error.message);
    assert(calls.includes("POST /model-providers/refresh"), "saved refresh still runs");
  });
});

await run(
  "catalog refresh: saved refresh failure is not hidden by another listing request",
  async () => {
    const { fake, calls } = backend({
      "POST /models/refresh": () => Response.json([{ id: "a" }]),
      "POST /model-providers/refresh": () =>
        Response.json({ error: "database is locked" }, { status: 500 }),
    });
    await withFetch(fake, async () => {
      const error = await failure(() => api.refreshModelCatalog());
      assert(
        error.message === "Failed to refresh provider connections: database is locked",
        error.message,
      );
      assert(calls.length === 2, `unexpected fallback request: ${calls.join()}`);
    });
  },
);

await run("catalog refresh: final models failure cannot substitute an earlier array", async () => {
  const { fake } = backend({
    "POST /models/refresh": () => Response.json([{ id: "a" }]),
    "POST /model-providers/refresh": () => Response.json([]),
    "GET /models": () => new Response("nope", { status: 500 }),
  });
  await withFetch(fake, async () => {
    const error = await failure(() => api.refreshModelCatalog());
    assert(error.message === "Failed to get models: nope", error.message);
  });
});

await run("catalog refresh: both requests settle before reporting failure", async () => {
  const { fake, calls } = backend({
    "POST /models/refresh": () => new Response("a", { status: 500 }),
    "POST /model-providers/refresh": () => new Response("b", { status: 500 }),
  });
  await withFetch(fake, async () => {
    const error = await failure(() => api.refreshModelCatalog());
    assert(error.message.includes("Failed to refresh models: a"), error.message);
    assert(calls.includes("POST /model-providers/refresh"), "saved refresh still runs");
  });
});
