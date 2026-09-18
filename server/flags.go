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

// FlagPatchOpenAIRaw makes OpenAI Responses services use the native apply_patch
// tool. It overrides patch-simple when both flags are enabled.
var FlagPatchOpenAIRaw = featureflags.Register(featureflags.Flag{
	Name:        "patch-openai-raw",
	Description: "Use the native OpenAI Responses apply_patch tool. Enable only when the selected endpoint supports tools with type apply_patch.",
	Default:     false,
})
