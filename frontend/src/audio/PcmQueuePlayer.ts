export class PcmQueuePlayer {
  private audioContext: AudioContext | null = null;
  private nextPlayTime = 0;
  private activeSources = new Set<AudioBufferSourceNode>();

  async init(): Promise<void> {
    if (this.audioContext == null) {
      this.audioContext = new AudioContext();
    }
    if (this.audioContext.state === 'suspended') {
      await this.audioContext.resume();
    }
  }

  async enqueue(pcm16le: ArrayBuffer, sampleRate: number, channels: number): Promise<void> {
    await this.init();
    const ctx = this.audioContext;
    if (ctx == null) {
      return;
    }

    const int16 = new Int16Array(pcm16le);
    if (int16.length === 0) {
      return;
    }

    const frameCount = Math.floor(int16.length / channels);
    if (frameCount <= 0) {
      return;
    }

    const buffer = ctx.createBuffer(channels, frameCount, sampleRate);
    for (let channel = 0; channel < channels; channel += 1) {
      const channelData = buffer.getChannelData(channel);
      for (let i = 0; i < frameCount; i += 1) {
        const sample = int16[i * channels + channel] ?? 0;
        channelData[i] = sample / 32768;
      }
    }

    const source = ctx.createBufferSource();
    source.buffer = buffer;
    source.connect(ctx.destination);
    this.activeSources.add(source);
    source.onended = () => {
      this.activeSources.delete(source);
    };

    const now = ctx.currentTime + 0.05;
    if (this.nextPlayTime < now) {
      this.nextPlayTime = now;
    }
    source.start(this.nextPlayTime);
    this.nextPlayTime += buffer.duration;
  }

  async waitForIdle(): Promise<void> {
    const ctx = this.audioContext;
    if (ctx == null || this.nextPlayTime <= 0) {
      return;
    }

    const remainingMs = Math.max(0, (this.nextPlayTime - ctx.currentTime) * 1000);
    if (remainingMs <= 0) {
      return;
    }
    await new Promise((resolve) => window.setTimeout(resolve, remainingMs));
  }

  resetQueue(): void {
    this.nextPlayTime = 0;
  }

  stopAll(): void {
    this.nextPlayTime = 0;
    for (const source of this.activeSources) {
      try {
        source.stop();
      } catch {
        // Ignore nodes that have already ended.
      }
      source.disconnect();
    }
    this.activeSources.clear();
  }
}
