#!/usr/bin/env node
// Rebuild dist/ whenever a UI source file changes.
//
// This exists to make UI work on Shelley itself a short loop. Normally dist/ is
// embedded into the Go binary, so changing a component means rebuilding the UI,
// rebuilding Go, and restarting the server before anything can be seen. Run
// this alongside a server started with SHELLEY_UI_DIR=ui/dist and a save plus a
// browser reload is the whole cycle.
//
// The full build reruns on every change rather than only the changed entry
// point: build.js gzips its outputs and deletes the originals, and writes a
// checksum manifest over all of them, so a partial rebuild would leave dist/
// internally inconsistent. Unminified rebuilds are quick enough that this is
// not worth optimizing until it hurts.

import { spawn } from "node:child_process";
import { watch } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));
const root = join(here, "..");
const srcDir = join(root, "src");

let building = false;
let queued = false;

function build() {
  if (building) {
    // A change arrived mid-build and may not be reflected in the output, so
    // remember to build once more rather than starting a concurrent build
    // that would race over the same files.
    queued = true;
    return;
  }
  building = true;
  const child = spawn("node", [join(here, "build.js"), "--watch"], {
    cwd: root,
    stdio: "inherit",
  });
  child.on("exit", (code) => {
    building = false;
    if (code !== 0) console.error(`build exited with ${code}; waiting for the next change`);
    if (queued) {
      queued = false;
      build();
    }
  });
}

// Debounce: editors write several files in quick succession, and a single
// save should not trigger a build per file.
let timer = null;
watch(srcDir, { recursive: true }, (_event, filename) => {
  if (filename && (filename.includes("node_modules") || filename.endsWith("~"))) return;
  clearTimeout(timer);
  timer = setTimeout(build, 100);
});

console.log(`watching ${srcDir}`);
build();
