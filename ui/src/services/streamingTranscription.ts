// AudioWorklet batches 100 ms of mono PCM16. Flush acknowledges the last
// partial batch before Stop commits the turn, so the last word is preserved.
const processorSource = `
class TranscriptionPCM extends AudioWorkletProcessor {
  constructor() {
    super();
    this.samples = new Int16Array(2400);
    this.offset = 0;
    this.stopped = false;
    this.port.onmessage = () => {
      this.stopped = true;
      this.flush();
      this.port.postMessage(null);
    };
  }
  flush() {
    if (!this.offset) return;
    const data = this.samples.slice(0, this.offset).buffer;
    this.port.postMessage(data, [data]);
    this.offset = 0;
  }
  process(inputs) {
    if (this.stopped) return false;
    const channels = inputs[0];
    if (!channels || !channels.length) return true;
    for (let i = 0; i < channels[0].length; i++) {
      let sample = 0;
      for (const channel of channels) sample += channel[i];
      sample = Math.max(-1, Math.min(1, sample / channels.length));
      this.samples[this.offset++] = Math.round(sample * (sample < 0 ? 32768 : 32767));
      if (this.offset === this.samples.length) this.flush();
    }
    return true;
  }
}
registerProcessor('transcription-pcm', TranscriptionPCM);
`;

export class StreamingTranscription {
  private closed = false;
  private stopping = false;
  private node?: AudioWorkletNode;
  private context?: AudioContext;
  private flush?: () => void;
  private fail!: (error: Error) => void;
  private complete!: (text: string) => void;
  private readonly result = new Promise<string>((resolve, reject) => {
    this.complete = resolve;
    this.fail = reject;
  });

  private constructor(
    private readonly socket: WebSocket,
    private readonly stream: MediaStream,
  ) {
    // Errors also reach onError; stop() can await the same rejected promise.
    void this.result.catch(() => {});
  }

  static async start(
    conversationID: string,
    stream: MediaStream,
    onText: (text: string) => void,
    onError: (error: Error) => void,
    signal: AbortSignal,
  ): Promise<StreamingTranscription> {
    const url = new URL(
      `/api/conversation/${encodeURIComponent(conversationID)}/transcription/stream`,
      location.href,
    );
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url);
    const session = new StreamingTranscription(socket, stream);
    let ready!: () => void;
    const connected = new Promise<void>((resolve) => {
      ready = resolve;
    });
    const fail = (error: Error) => {
      if (session.closed) return;
      session.fail(error);
      session.close();
      onError(error);
    };
    let partial = "";
    socket.onmessage = (message) => {
      try {
        const event = JSON.parse(String(message.data)) as {
          type: string;
          text?: string;
          message?: string;
        };
        switch (event.type) {
          case "ready":
            ready();
            break;
          case "delta":
            partial += event.text ?? "";
            onText(partial);
            break;
          case "completed":
            if (!session.stopping || !event.text?.trim())
              throw new Error("Invalid final transcript");
            onText(event.text);
            session.complete(event.text);
            session.close();
            break;
          case "error":
            throw new Error(event.message || "Transcription failed");
          default:
            throw new Error("Unexpected transcription event");
        }
      } catch (error) {
        fail(error instanceof Error ? error : new Error(String(error)));
      }
    };
    socket.onerror = () => fail(new Error("Could not connect to streaming transcription"));
    socket.onclose = () => fail(new Error("Streaming transcription disconnected"));
    const abort = () => {
      session.fail(new DOMException("Recording cancelled", "AbortError"));
      session.close();
    };
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
    const timer = setTimeout(() => fail(new Error("Timed out starting transcription")), 25_000);
    try {
      await Promise.race([connected, session.result]);
      signal.throwIfAborted();
      const context = new AudioContext({ sampleRate: 24000 });
      session.context = context;
      const moduleURL = URL.createObjectURL(
        new Blob([processorSource], { type: "text/javascript" }),
      );
      try {
        await context.audioWorklet.addModule(moduleURL);
      } finally {
        URL.revokeObjectURL(moduleURL);
      }
      signal.throwIfAborted();
      if (session.closed) throw new Error("Transcription disconnected while starting microphone");
      const node = new AudioWorkletNode(context, "transcription-pcm");
      session.node = node;
      node.onprocessorerror = () => fail(new Error("Microphone audio processing failed"));
      node.port.onmessage = (event: MessageEvent<ArrayBuffer | null>) => {
        if (session.closed) return;
        if (event.data === null) {
          session.flush?.();
          return;
        }
        if (socket.readyState !== WebSocket.OPEN || socket.bufferedAmount > 1_048_576) {
          fail(new Error("Transcription connection cannot keep up with the microphone"));
          return;
        }
        socket.send(event.data);
      };
      context.createMediaStreamSource(stream).connect(node);
      // The processor emits silence; connecting it keeps the audio graph active.
      node.connect(context.destination);
      await context.resume();
      return session;
    } catch (error) {
      session.close();
      throw error;
    } finally {
      clearTimeout(timer);
      // Cancellation after start is handled by the owner's close().
      signal.removeEventListener("abort", abort);
    }
  }

  async stop(): Promise<string> {
    if (this.closed || this.stopping) throw new Error("Transcription is not recording");
    this.stopping = true;
    const timeout = setTimeout(
      () => this.fail(new Error("Timed out finalizing transcription")),
      20_000,
    );
    try {
      const flushed = new Promise<void>((resolve) => {
        this.flush = resolve;
      });
      this.node!.port.postMessage("flush");
      await Promise.race([flushed, this.result]);
      if (this.closed) return await this.result;
      for (const track of this.stream.getTracks()) track.stop();
      this.socket.send(JSON.stringify({ type: "commit" }));
      return await this.result;
    } finally {
      clearTimeout(timeout);
      this.close();
    }
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.fail(new DOMException("Recording cancelled", "AbortError"));
    this.socket.close();
    this.node?.disconnect();
    if (this.context && this.context.state !== "closed") void this.context.close();
    for (const track of this.stream.getTracks()) track.stop();
  }
}
