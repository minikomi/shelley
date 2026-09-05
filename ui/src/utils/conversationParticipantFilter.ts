interface ConversationWithParticipantList {
  parent_conversation_id?: string | null;
  is_draft?: boolean;
  participants?: { email: string; message_count: number }[] | null;
}

function foldEmail(email: string | null | undefined): string {
  return email?.trim().toLowerCase() ?? "";
}

export function hasOtherParticipant(
  conversations: readonly ConversationWithParticipantList[],
  currentUserEmail: string | null | undefined,
): boolean {
  const current = foldEmail(currentUserEmail);
  if (!current) return false;
  return conversations.some((conversation) =>
    conversation.participants?.some(({ email }) => {
      const participant = foldEmail(email);
      return participant !== "" && participant !== current;
    }),
  );
}

// True when some conversation itself has more than one participant. Distinct
// from hasOtherParticipant: two single-user conversations by different people
// satisfy that but not this.
export function hasMultiParticipantConversation(
  conversations: readonly ConversationWithParticipantList[],
): boolean {
  return conversations.some((conversation) => participantEmails(conversation).length > 1);
}

// The folded, sorted, deduped participant emails of a conversation.
function participantEmails(conversation: ConversationWithParticipantList): string[] {
  const folded = new Set(
    conversation.participants?.map(({ email }) => foldEmail(email)).filter(Boolean) ?? [],
  );
  return [...folded].sort();
}

export function filterConversationsByParticipantQuery<T extends ConversationWithParticipantList>(
  conversations: T[],
  selectedUsers: readonly string[],
  includeUnattributed: boolean,
): T[] {
  const users = new Set(selectedUsers.map(foldEmail).filter(Boolean));
  if (users.size === 0 && !includeUnattributed) return conversations;
  return conversations.filter((conversation) => {
    // Subagents ride with their parent; a draft has no messages yet, so no
    // participants, and must not vanish under the default current-user filter.
    if (conversation.parent_conversation_id || conversation.is_draft) return true;
    const participants =
      conversation.participants?.map(({ email }) => foldEmail(email)).filter(Boolean) ?? [];
    if (includeUnattributed) return users.size === 0 && participants.length === 0;
    const participantSet = new Set(participants);
    return [...users].every((email) => participantSet.has(email));
  });
}
