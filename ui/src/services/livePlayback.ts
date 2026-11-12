// A continuous WebRTC track has no per-utterance ended event. Measure the
// remote audio instead, retaining short pauses within a spoken thought.
export class LivePlaybackActivity {
  active = false;
  private lastSound = -Infinity;

  sample(samples: Float32Array, now: number): boolean {
    let energy = 0;
    for (const sample of samples) energy += sample * sample;
    if (Math.sqrt(energy / samples.length) > 0.003) this.lastSound = now;
    const active = now - this.lastSound < 1200;
    const changed = active !== this.active;
    this.active = active;
    return changed;
  }
}

export function watchLivePlayback(
  stream: MediaStream,
  onActive: (active: boolean) => void,
  onError: (error: Error) => void,
): () => void {
  const context = new AudioContext();
  const source = context.createMediaStreamSource(stream);
  const analyser = context.createAnalyser();
  analyser.fftSize = 1024;
  source.connect(analyser);
  const samples = new Float32Array(analyser.fftSize);
  const activity = new LivePlaybackActivity();
  const timer = setInterval(() => {
    analyser.getFloatTimeDomainData(samples);
    if (activity.sample(samples, performance.now())) onActive(activity.active);
  }, 50);
  void context.resume().catch((error: unknown) => {
    onError(error instanceof Error ? error : new Error(String(error)));
  });
  return () => {
    clearInterval(timer);
    source.disconnect();
    analyser.disconnect();
    void context.close();
  };
}
