---
name: focused-ui-iteration
description: Use when iterating on the appearance of a web UI — adjusting CSS, spacing, colors, or component markup — and especially when each change is costing a rebuild and a server restart before you can see the result.
---

Editing a stylesheet, rebuilding, restarting the server, reloading the page,
and hunting for the component again is a slow loop for a change that is often
one line of CSS. Most of that work is avoidable. The goal is to see the effect
of a change in seconds, and to spend the rebuild only once, at the end.

## The loop

1. Get the page in front of you without rebuilding it on every edit.
2. Pin down the smallest selector that contains what you are changing.
3. Change one thing.
4. Screenshot that selector.
5. Repeat 3–4 until it looks right.
6. Write the result into source, then rebuild and verify once.

## 1. Prefer a dev server with hot reload

If the project has a watch mode — Vite, Next.js, webpack, `pnpm dev` — run it
and point the browser at it. CSS and component edits then appear without a
build or a restart. Check `package.json` scripts rather than guessing the
command; projects differ, and a wrong guess costs more than a look.

A separate API or backend process usually keeps running untouched while the
frontend reloads. Only restart the piece that actually changed.

If the project has no watch mode, the rebuild is unavoidable — but see step 3,
which removes most of the need for it while you are still deciding what the
CSS should say.

On an exe.dev VM a dev server needs the VM hostname in its allowed-hosts
configuration or the browser's live-reload socket will fail. The
`node-and-js-frameworks` skill covers that setup.

### Working on Shelley's own UI

Shelley embeds `ui/dist` into the Go binary, so by default a one-line CSS
change costs a UI build, a Go build, and a restart. Two commands remove that:

```bash
make ui-watch   # rebuilds ui/dist on save
make serve-ui   # serves assets from ui/dist on disk, on port 8003
```

`serve-ui` sets `SHELLEY_UI_DIR`, which makes the server read each asset from
the directory per request instead of from the binary, and drops ETags so the
browser cannot 304 its way into the previous build. Edit a component or
stylesheet, wait for the watch to report a build, reload the page. No Go build,
no restart.

It uses the predictable model and a scratch database, so it cannot disturb real
conversations. Changes to Go code still need a rebuild and restart — this only
covers the frontend.

## 2. Pin the selector, not the page

Find the smallest stable selector that contains the component: a `data-testid`,
a component root class, an id. Prefer one that will not change as you edit the
markup.

Then screenshot *that*, not the whole page:

```json
{"action": "screenshot", "selector": "[data-testid='settings-panel']"}
```

A cropped screenshot is larger on screen, shows the thing you are changing,
and does not force you to re-find the component in a full-page image every
time. Take a full-page screenshot to locate the component at the start, and
again at the end to check you did not disturb the surrounding layout.

Do not copy the component into a standalone HTML file to iterate on it. It
loses inherited styles, fonts, theme variables, application state, and
responsive context, so it stops being evidence about the real UI.

## 3. Experiment with injected CSS before editing files

While you are still deciding what a rule should be, try it in the live page:

```json
{"action": "inject_css", "css": ".settings-panel { gap: 12px; padding: 16px 20px }"}
```

This applies immediately with no edit, no build, and no reload. Iterate here
until it looks right — this is where the loop gets genuinely tight.

Three things to keep straight:

- The injection **overrides the real stylesheets**. While it is live, the page
  is not showing what your source files produce. Screenshots say so, and you
  should believe them: do not conclude a bug is fixed, or that source is
  correct, while an injection is active.
- Each call **replaces** the previous injection rather than adding to it, so
  send the complete set of rules you want, not a diff.
- The injection **does not survive navigation or a full reload**. Hot module
  replacement leaves it alone.

When a rule works, copy it into the real stylesheet, clear the injection with
an empty `css`, reload, and confirm the page still looks right without the
override. If it does not, the rule landed in the wrong file or is losing to a
more specific selector — which is exactly the bug the clean check exists to
catch.

Injected CSS is for experimentation only. It is not a fix; nothing about it
persists.

### Changing markup, not just styling

Sometimes the change is structural. `browser eval` can mutate the DOM directly
(`classList.add`, `setAttribute`, `insertAdjacentHTML`), and this works better
than it sounds — but only for some kinds of change.

Vue 3 and React 18 behave identically here: a virtual DOM only overwrites the
specific properties it owns. Measured on both:

| Mutation | Survives a re-render? |
| --- | --- |
| Insert a new child element | yes |
| Add a new attribute to a bound element | yes |
| Add a class to an element with no `:class`/`className` binding | yes |
| Change the text of a static node | yes |
| Add a class to an element that *has* a class binding | no |
| Change the text of a bound node | no |

So **adding** markup is reliable, and **overriding something the framework
binds** is not: the framework rewrites that property on its next patch and your
change disappears. Nothing errors when this happens — an unrelated state update
silently reverts it, which is easy to misread as the change not having worked.

When you need to override a bound class, use injected CSS instead. Stylesheets
are not in the virtual DOM, so a rule targeting the element wins regardless of
what the binding sets.

Two cautions. A DOM mutation has no clean undo — reload the page to get back to
a known state. And unlike injected CSS, screenshots cannot tell that the DOM
was mutated, so it is on you to remember that the page no longer matches
source. If the markup change is what you are actually shipping, edit the
component and let the watch reload it; DOM mutation is only for deciding
whether a structural change is worth making.

## 4. Finish properly

Once the appearance is right:

- Write every change into source, with no injection still live.
- Run the project's type checks, linter, and tests.
- Do the full build and restart, if the project needs one.
- Screenshot the component, and the whole page, to confirm the real build
  matches what you converged on.
