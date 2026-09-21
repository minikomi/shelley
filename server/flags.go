package server

import "shelley.exe.dev/featureflags"

// FlagPerformanceHUD overlays a small heads-up display in the web UI showing
// live counters of hot reactive recomputations (message coalescing, render
// model rebuilds, markdown parses, scroll/resize handler fires, store
// notifications, ...). The counters themselves are always collected — they
// are plain Map increments, cheap enough to leave on — and are accessible
// from the browser console via window.__shelleyPerf regardless of the flag.
// The flag only controls whether the HUD overlay renders.
var FlagPerformanceHUD = featureflags.Register(featureflags.Flag{
	Name:        "performance-hud",
	Description: "Show a heads-up display of UI recomputation counters (also available via __shelleyPerf in the console).",
	Default:     false,
})

// FlagCompactSendThresholds defaults the composer to Compact and send once
// the context reaches 200k tokens.
var FlagCompactSendThresholds = featureflags.Register(featureflags.Flag{
	Name:        "compact-send-thresholds",
	Description: "Default the composer to Compact and send once the context reaches 200k tokens.",
	Default:     false,
})

// FlagPatchSimple switches the patch tool from its full nested patches schema
// to a simplified path-and-edits replacement schema.
var FlagPatchSimple = featureflags.Register(featureflags.Flag{
	Name:        "patch-simple",
	Description: "Use a simplified path and edits array for atomic exact-text replacements. When off, use the full nested patches schema.",
	Default:     false,
})

// FlagTaskGraph exposes the task_graph tool to top-level conversations.
var FlagTaskGraph = featureflags.Register(featureflags.Flag{
	Name:        "task-graph",
	Description: "Expose the task_graph tool for persisted, dependency-ordered subagent delegation with a live dock in the conversation.",
	Default:     false,
})

// FlagPatchOpenAIRaw lets capable direct OpenAI Responses models use the raw,
// grammar-constrained Codex apply_patch tool. It overrides patch-simple when
// both flags are enabled and has no effect on unsupported providers.
var FlagPatchOpenAIRaw = featureflags.Register(featureflags.Flag{
	Name:        "patch-openai-raw",
	Description: "Use raw grammar-constrained apply_patch for capable direct OpenAI Responses models, overriding the full or simplified nested patch schema.",
	Default:     false,
})
