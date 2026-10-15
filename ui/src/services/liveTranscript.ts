export interface LiveTranscriptTurn {
  user: boolean;
  text: string;
}

interface Fragment extends LiveTranscriptTurn {
  through: number;
}

export class LiveTranscript {
  private count = 0;
  private consumed = 0;
  private fragments: Fragment[] = [];
  private handoffs = new Map<number, string>();

  append(user: boolean, text: string) {
    this.fragments.push({ through: ++this.count, user, text });
  }

  handoff(through: number, message: string) {
    if (through > this.consumed) this.handoffs.set(through, message);
  }

  reconcile(savedMessages: readonly string[]) {
    const saved = new Set(savedMessages);
    for (const [through, message] of this.handoffs) {
      if (saved.has(message)) this.consumed = Math.max(this.consumed, through);
    }
    this.fragments = this.fragments.filter((fragment) => fragment.through > this.consumed);
    for (const through of this.handoffs.keys()) {
      if (through <= this.consumed) this.handoffs.delete(through);
    }
  }

  get turns(): LiveTranscriptTurn[] {
    const turns: LiveTranscriptTurn[] = [];
    for (const fragment of this.fragments) {
      const last = turns.at(-1);
      if (last?.user === fragment.user) last.text += fragment.text;
      else turns.push({ user: fragment.user, text: fragment.text });
    }
    return turns.filter((turn) => turn.text.trim() !== "");
  }
}
