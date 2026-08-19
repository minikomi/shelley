package server

import "shelley.exe.dev/featureflags"

// FlagToolPills toggles the iOS-style pill rendering of tool bursts in the
// web conversation UI. When false (the default), each tool call renders as
// a full-width CoalescedToolCall card as before. When true, consecutive
// non-auto-expand tool calls collapse into a wrapped row of compact pills;
// tapping a pill opens the full card in a modal.
//
// Auto-expand tools (patch, screenshot, read_image, output_iframe) are
// unaffected — they continue to render inline regardless of this flag.
var FlagToolPills = featureflags.Register(featureflags.Flag{
	Name:        "tool-pills",
	Description: "Render bursts of tool calls as compact pills (iOS-style). Click a pill to open the full tool card in a modal.",
	Default:     false,
})

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

// FlagAutomaticCompaction switches compaction from a manual, task-report
// summarizer to an automatic, checkpoint-style one. When false (the default),
// /compact behaves exactly as before: the user triggers it, and the summary is
// pi's chronological task report.
//
// When true, two things change together, because neither is much use alone:
//
//  1. Compaction triggers itself. A turn ending over
//     compactionThresholdFraction of the model's context window schedules a
//     compaction, so a long conversation shrinks without the user watching a
//     usage bar (see maybeScheduleCompaction).
//  2. The summary becomes a topic-based checkpoint whose claims carry [seq:N]
//     pointers back to the messages they came from. An automatic summarizer is
//     one the user did not ask for and does not review, so it has to be
//     recoverable: the summarized rows stay in the database, and the summary
//     tells the reading model how to query them (see
//     checkpointCompactionSummarySuffix).
//
// The underlying mechanism is unchanged either way — same cut point, same
// verbatim recent tail, same generation bump, same rollback on failure.
var FlagAutomaticCompaction = featureflags.Register(featureflags.Flag{
	Name:        "automatic-compaction",
	Description: "Compact long conversations automatically, using topic-based checkpoint summaries with [seq:N] pointers back to the original messages.",
	Default:     false,
})
