// latestGate.test.ts — overlapping loads: only the newest result applies.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import { latestGate } from "./latestGate";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

async function run(name: string, fn: () => void | Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

await run("a slow older load cannot overwrite a newer one", async () => {
  const gate = latestGate();
  let shown = "";
  const load = async (data: Promise<string>) => {
    const current = gate.begin();
    const value = await data;
    if (current()) shown = value;
  };
  const older = deferred<string>();
  const newer = deferred<string>();
  const first = load(older.promise);
  const second = load(newer.promise);
  newer.resolve("new");
  await second;
  older.resolve("old");
  await first;
  assert(shown === "new", `shown = ${shown}, want new`);
});

await run("the latest of coalesced loads applies even when it finishes last", async () => {
  const gate = latestGate();
  const applied: string[] = [];
  const load = async (data: Promise<string>) => {
    const current = gate.begin();
    const value = await data;
    if (current()) applied.push(value);
  };
  const a = deferred<string>();
  const b = deferred<string>();
  const c = deferred<string>();
  const loads = [load(a.promise), load(b.promise), load(c.promise)];
  a.resolve("a");
  b.resolve("b");
  c.resolve("c");
  await Promise.all(loads);
  assert(applied.join() === "c", `applied = ${applied.join()}`);
});

await run("supersede orphans loads already in flight", async () => {
  const gate = latestGate();
  const current = gate.begin();
  gate.supersede();
  assert(!current(), "in-flight load survived supersede");
  assert(gate.begin()(), "a load begun after supersede is current");
});
