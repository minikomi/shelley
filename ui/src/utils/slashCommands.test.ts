import assert from "node:assert/strict";
import { SLASH_COMMANDS, slashCommandsForConversation } from "./slashCommands";

function commands(child: boolean, isExeDev = false): string[] {
  return slashCommandsForConversation(child, isExeDev).map((item) => item.command);
}

assert(commands(false).includes(SLASH_COMMANDS.BTW.command), "top-level conversations offer /btw");
assert(
  commands(false).includes(SLASH_COMMANDS.TOUR.command),
  "top-level conversations offer /tour",
);
assert(
  commands(false).includes(SLASH_COMMANDS.TRANSCRIPTION.command),
  "top-level conversations offer /transcription",
);
assert(!commands(true).includes(SLASH_COMMANDS.BTW.command), "child conversations omit /btw");
assert(!commands(true).includes(SLASH_COMMANDS.TOUR.command), "child conversations omit /tour");
assert(commands(true).includes(SLASH_COMMANDS.FORK.command), "child conversations retain commands");
assert(!commands(false).includes(SLASH_COMMANDS.PREVIEW.command), "local installs omit /preview");
assert(commands(false, true).includes(SLASH_COMMANDS.PREVIEW.command), "exe.dev offers /preview");
