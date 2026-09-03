interface ConversationWithParticipantList {
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

export function filterConversationsByParticipantQuery<T extends ConversationWithParticipantList>(
  conversations: T[],
  selectedUsers: readonly string[],
  includeUnattributed: boolean,
): T[] {
  const users = new Set(selectedUsers.map(foldEmail).filter(Boolean));
  if (users.size === 0 && !includeUnattributed) return conversations;
  return conversations.filter((conversation) => {
    const participants =
      conversation.participants?.map(({ email }) => foldEmail(email)).filter(Boolean) ?? [];
    if (includeUnattributed) return users.size === 0 && participants.length === 0;
    const participantSet = new Set(participants);
    return [...users].every((email) => participantSet.has(email));
  });
}
