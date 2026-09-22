import assert from "node:assert/strict";
import { SLASH_COMMANDS, slashCommandsForConversation } from "./slashCommands";

function commands(child: boolean, previewEnabled = false): string[] {
  return slashCommandsForConversation(child, previewEnabled).map((item) => item.command);
}

assert(commands(false).includes(SLASH_COMMANDS.BTW.command), "top-level conversations offer /btw");
assert(
  commands(false).includes(SLASH_COMMANDS.TRANSCRIPTION.command),
  "top-level conversations offer /transcription",
);
assert(!commands(true).includes(SLASH_COMMANDS.BTW.command), "child conversations omit /btw");
assert(commands(true).includes(SLASH_COMMANDS.FORK.command), "child conversations retain commands");

assert(
  commands(false).includes(SLASH_COMMANDS.SKILLS.command),
  "top-level conversations offer /skills",
);
assert(commands(true).includes(SLASH_COMMANDS.SKILLS.command), "child conversations offer /skills");
assert(SLASH_COMMANDS.SKILLS.takesArgs, "/skills opens an argument picker instead of dispatching");
assert(
  !commands(false).includes(SLASH_COMMANDS.PREVIEW.command),
  "disabled installs omit /preview",
);
assert(commands(false, true).includes(SLASH_COMMANDS.PREVIEW.command), "the flag offers /preview");
