import { useEffect, useMemo, useRef, useState, type Dispatch, type MutableRefObject, type SetStateAction } from 'react';
import { PcmQueuePlayer } from './audio/PcmQueuePlayer';
import { ReadyCuePlayer } from './audio/ReadyCuePlayer';
import { encodePcm16 } from './audio/pcm';
import { useVoiceSocket, type ConnectionStatus } from './useVoiceSocket';

type ViewMode = 'test' | 'qa';

type VoiceOption = {
  id: string;
  displayName: string;
  provider: string;
  default: boolean;
};

type Status = 'READY' | 'CONNECTING' | 'STREAMING' | 'STOPPED' | 'ERROR';
type QaStatus = 'IDLE' | 'STARTING' | 'LISTENING' | 'THINKING' | 'SPEAKING' | 'ERROR';
type CaptureOwner = 'test' | 'qa' | null;

type ServerEvent = {
  type: string;
  sessionId?: string;
  taskId?: string;
  taskType?: string;
  chatId?: string;
  mode?: 'local' | 'llm';
  reason?: string;
  code?: string;
  message?: string;
  text?: string;
  voice?: string;
  voiceDisplayName?: string;
  sampleRate?: number;
  channels?: number;
  seq?: number;
  byteLength?: number;
};

type ClientGateSettings = {
  enabled?: boolean;
  rmsThreshold?: number;
  openHoldMs?: number;
  closeHoldMs?: number;
  preRollMs?: number;
};

type ClientGateConfig = {
  enabled: boolean;
  rmsThreshold: number;
  openHoldMs: number;
  closeHoldMs: number;
  preRollMs: number;
};

type CapabilitiesResponse = {
  websocketPath?: string;
  asr?: {
    configured?: boolean;
    defaults?: {
      sampleRate?: number;
      language?: string;
      clientGate?: ClientGateSettings;
      turnDetection?: {
        type?: string;
        threshold?: number;
        silenceDurationMs?: number;
      };
    };
  };
  tts?: {
    modes?: string[];
    defaultMode?: 'local' | 'llm';
    runnerConfigured?: boolean;
    speechRateDefault?: number;
    audioFormat?: {
      sampleRate?: number;
      channels?: number;
    };
  };
};

type AudioCaptureRefs = {
  streamRef: MutableRefObject<MediaStream | null>;
  audioContextRef: MutableRefObject<AudioContext | null>;
  sourceRef: MutableRefObject<MediaStreamAudioSourceNode | null>;
  processorRef: MutableRefObject<ScriptProcessorNode | null>;
  captureStartedRef: MutableRefObject<boolean>;
  remainRef: MutableRefObject<Uint8Array>;
  gateStateRef: MutableRefObject<ClientGateRuntime>;
};

type PendingBinary = {
  taskId: string;
  seq: number;
};

type TtsAudioSummary = {
  chunks: number;
  bytes: number;
  startedAt: number;
  receivingLogged: boolean;
};

const FRAME_BYTES = 640;
const PCM16_BYTES_PER_MS = 32;
const QA_SEND_PAUSE_DEFAULT_SECONDS = 1.5;
const QA_SEND_PAUSE_MIN_SECONDS = 0.5;
const QA_SEND_PAUSE_MAX_SECONDS = 10.0;
const QA_READY_CUE_DELAY_MS = 300;
const DEFAULT_CLIENT_GATE: ClientGateConfig = {
  enabled: true,
  rmsThreshold: 0.008,
  openHoldMs: 120,
  closeHoldMs: 480,
  preRollMs: 240
};
const TEST_ASR_TASK_ID = 'asr-test';
const TEST_TTS_TASK_ID = 'tts-test';
const QA_ASR_TASK_ID = 'qa-asr';
const QA_TTS_TASK_ID = 'qa-tts';

type ClientGateRuntime = {
  config: ClientGateConfig;
  isOpen: boolean;
  openAccumulatedMs: number;
  closeAccumulatedMs: number;
  preRollChunks: Uint8Array[];
  preRollBytes: number;
};

function isLocalDevHost(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1';
}

function resolveDefaultWsUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const { host } = window.location;
  return `${protocol}//${host}/api/voice/ws`;
}

function resolveWsUrl(raw: string): string {
  if (raw.trim()) {
    return raw.trim();
  }
  return resolveDefaultWsUrl();
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  const chunkSize = 0x8000;
  for (let i = 0; i < bytes.length; i += chunkSize) {
    const chunk = bytes.subarray(i, i + chunkSize);
    binary += String.fromCharCode(...chunk);
  }
  return btoa(binary);
}

function normalizeClientGateConfig(settings?: ClientGateSettings): ClientGateConfig {
  const next = settings ?? {};
  const rmsThreshold =
    typeof next.rmsThreshold === 'number' && Number.isFinite(next.rmsThreshold) && next.rmsThreshold >= 0
      ? next.rmsThreshold
      : DEFAULT_CLIENT_GATE.rmsThreshold;
  const openHoldMs =
    typeof next.openHoldMs === 'number' && Number.isFinite(next.openHoldMs) && next.openHoldMs >= 0
      ? next.openHoldMs
      : DEFAULT_CLIENT_GATE.openHoldMs;
  const closeHoldMs =
    typeof next.closeHoldMs === 'number' && Number.isFinite(next.closeHoldMs) && next.closeHoldMs >= 0
      ? next.closeHoldMs
      : DEFAULT_CLIENT_GATE.closeHoldMs;
  const preRollMs =
    typeof next.preRollMs === 'number' && Number.isFinite(next.preRollMs) && next.preRollMs >= 0
      ? next.preRollMs
      : DEFAULT_CLIENT_GATE.preRollMs;

  return {
    enabled: next.enabled ?? DEFAULT_CLIENT_GATE.enabled,
    rmsThreshold,
    openHoldMs,
    closeHoldMs,
    preRollMs
  };
}

function createClientGateRuntime(config: ClientGateConfig): ClientGateRuntime {
  return {
    config,
    isOpen: false,
    openAccumulatedMs: 0,
    closeAccumulatedMs: 0,
    preRollChunks: [],
    preRollBytes: 0
  };
}

function resetClientGateRuntime(runtime: ClientGateRuntime, config?: ClientGateConfig) {
  runtime.config = config ?? runtime.config;
  runtime.isOpen = false;
  runtime.openAccumulatedMs = 0;
  runtime.closeAccumulatedMs = 0;
  runtime.preRollChunks = [];
  runtime.preRollBytes = 0;
}

function calculateRms(samples: Float32Array): number {
  if (samples.length === 0) {
    return 0;
  }

  let sum = 0;
  for (let i = 0; i < samples.length; i += 1) {
    sum += samples[i] * samples[i];
  }
  return Math.sqrt(sum / samples.length);
}

function bufferClientGatePreRoll(runtime: ClientGateRuntime, bytes: Uint8Array) {
  if (runtime.config.preRollMs <= 0 || bytes.length === 0) {
    runtime.preRollChunks = [];
    runtime.preRollBytes = 0;
    return;
  }

  runtime.preRollChunks.push(bytes);
  runtime.preRollBytes += bytes.length;

  const maxBytes = Math.max(0, Math.floor(runtime.config.preRollMs * PCM16_BYTES_PER_MS));
  while (runtime.preRollBytes > maxBytes && runtime.preRollChunks.length > 0) {
    const first = runtime.preRollChunks[0];
    const overflow = runtime.preRollBytes - maxBytes;
    if (first.length <= overflow) {
      runtime.preRollChunks.shift();
      runtime.preRollBytes -= first.length;
      continue;
    }
    runtime.preRollChunks[0] = first.slice(overflow);
    runtime.preRollBytes -= overflow;
  }
}

function flushClientGatePreRoll(
  runtime: ClientGateRuntime,
  remainRef: MutableRefObject<Uint8Array>,
  onChunk: (chunk: Uint8Array) => void
) {
  for (const chunk of runtime.preRollChunks) {
    emitChunkedAudio(chunk, remainRef, onChunk);
  }
  runtime.preRollChunks = [];
  runtime.preRollBytes = 0;
}

function handleCapturedPcm(
  refs: AudioCaptureRefs,
  input: Float32Array,
  bytes: Uint8Array,
  onChunk: (chunk: Uint8Array) => void
) {
  const runtime = refs.gateStateRef.current;
  if (!runtime.config.enabled) {
    emitChunkedAudio(bytes, refs.remainRef, onChunk);
    return;
  }

  const frameDurationMs = bytes.length / PCM16_BYTES_PER_MS;
  if (frameDurationMs <= 0) {
    return;
  }

  const aboveThreshold = calculateRms(input) >= runtime.config.rmsThreshold;

  if (!runtime.isOpen) {
    bufferClientGatePreRoll(runtime, bytes);
    runtime.closeAccumulatedMs = 0;
    runtime.openAccumulatedMs = aboveThreshold ? runtime.openAccumulatedMs + frameDurationMs : 0;
    if (!aboveThreshold || runtime.openAccumulatedMs < runtime.config.openHoldMs) {
      return;
    }
    runtime.isOpen = true;
    runtime.openAccumulatedMs = 0;
    flushClientGatePreRoll(runtime, refs.remainRef, onChunk);
    return;
  }

  emitChunkedAudio(bytes, refs.remainRef, onChunk);
  if (aboveThreshold) {
    runtime.closeAccumulatedMs = 0;
    return;
  }

  runtime.closeAccumulatedMs += frameDurationMs;
  if (runtime.closeAccumulatedMs >= runtime.config.closeHoldMs) {
    runtime.isOpen = false;
    runtime.openAccumulatedMs = 0;
    runtime.closeAccumulatedMs = 0;
    runtime.preRollChunks = [];
    runtime.preRollBytes = 0;
  }
}

function cleanupAudioCapture(refs: AudioCaptureRefs) {
  refs.captureStartedRef.current = false;
  if (refs.processorRef.current) {
    refs.processorRef.current.disconnect();
    refs.processorRef.current.onaudioprocess = null;
    refs.processorRef.current = null;
  }
  if (refs.sourceRef.current) {
    refs.sourceRef.current.disconnect();
    refs.sourceRef.current = null;
  }
  if (refs.audioContextRef.current) {
    void refs.audioContextRef.current.close();
    refs.audioContextRef.current = null;
  }
  if (refs.streamRef.current) {
    refs.streamRef.current.getTracks().forEach((track) => track.stop());
    refs.streamRef.current = null;
  }
  refs.remainRef.current = new Uint8Array(0);
  resetClientGateRuntime(refs.gateStateRef.current);
}

function emitChunkedAudio(
  bytes: Uint8Array,
  remainRef: MutableRefObject<Uint8Array>,
  onChunk: (chunk: Uint8Array) => void
) {
  const merged = new Uint8Array(remainRef.current.length + bytes.length);
  merged.set(remainRef.current, 0);
  merged.set(bytes, remainRef.current.length);

  let offset = 0;
  while (offset + FRAME_BYTES <= merged.length) {
    onChunk(merged.slice(offset, offset + FRAME_BYTES));
    offset += FRAME_BYTES;
  }
  remainRef.current = merged.slice(offset);
}

async function initializeAudioCapture(
  refs: AudioCaptureRefs,
  onChunk: (chunk: Uint8Array) => void,
  onStarted: () => void,
  onError: (message: string) => void
) {
  if (refs.captureStartedRef.current) {
    return;
  }
  refs.captureStartedRef.current = true;

  try {
    const mediaStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    refs.streamRef.current = mediaStream;

    const AudioContextCtor =
      window.AudioContext || (window as typeof window & { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (AudioContextCtor == null) {
      throw new Error('当前浏览器不支持 AudioContext');
    }

    const audioContext = new AudioContextCtor();
    refs.audioContextRef.current = audioContext;

    const source = audioContext.createMediaStreamSource(mediaStream);
    refs.sourceRef.current = source;

    const processor = audioContext.createScriptProcessor(4096, 1, 1);
    refs.processorRef.current = processor;

    processor.onaudioprocess = (event) => {
      if (!refs.captureStartedRef.current) {
        return;
      }
      const input = event.inputBuffer.getChannelData(0);
      const pcm16 = encodePcm16(input, audioContext.sampleRate, 16000);
      handleCapturedPcm(refs, input, new Uint8Array(pcm16.buffer), onChunk);
    };

    source.connect(processor);
    processor.connect(audioContext.destination);
    onStarted();
  } catch (error) {
    cleanupAudioCapture(refs);
    onError(`麦克风初始化失败: ${error instanceof Error ? error.message : String(error)}`);
  }
}

function statusClass(status: Status | ConnectionStatus): string {
  return `status ${status.toLowerCase()}`;
}

function qaStatusClass(status: QaStatus): string {
  return `status ${status.toLowerCase()}`;
}

function isTaskActive(status: Status): boolean {
  return status === 'CONNECTING' || status === 'STREAMING';
}

function formatByteSize(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  return `${(bytes / 1024).toFixed(1)} KB`;
}

function formatDuration(ms: number): string {
  if (ms < 1000) {
    return `${ms} ms`;
  }
  return `${(ms / 1000).toFixed(1)} s`;
}

function clampSpeechRate(rate: number): number {
  if (Number.isNaN(rate)) {
    return 1.2;
  }
  return Math.max(0.5, Math.min(2.0, rate));
}

function clampQaSendPauseSeconds(seconds: number): number {
  if (Number.isNaN(seconds)) {
    return QA_SEND_PAUSE_DEFAULT_SECONDS;
  }
  return Math.max(QA_SEND_PAUSE_MIN_SECONDS, Math.min(QA_SEND_PAUSE_MAX_SECONDS, seconds));
}

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function mergeQaUtterance(current: string, next: string): string {
  const left = current.trimEnd();
  const right = next.trimStart();
  if (!left) {
    return right;
  }
  if (!right) {
    return left;
  }
  const needsSpace = /[A-Za-z0-9]$/.test(left) && /^[A-Za-z0-9]/.test(right);
  return `${left}${needsSpace ? ' ' : ''}${right}`;
}

function normalizeQaUtteranceForLength(text: string): string {
  return text
    .trim()
    .replace(
      /[\s\u3000!"#$%&'()*+,./:;<=>?@[\\\]^_`{|}~\-，。！？、；：,.·!?"“”'‘’（）()【】《》〈〉「」『』…—]+/g,
      ''
    );
}

export default function App() {
  const [wsUrlInput, setWsUrlInput] = useState(() => resolveDefaultWsUrl());
  const [viewMode, setViewMode] = useState<ViewMode>('test');
  const [capabilities, setCapabilities] = useState<CapabilitiesResponse | null>(null);
  const [voices, setVoices] = useState<VoiceOption[]>([]);
  const [voicesLoaded, setVoicesLoaded] = useState(false);
  const [voicesError, setVoicesError] = useState('');
  const [selectedVoice, setSelectedVoice] = useState('');
  const [ttsSpeechRate, setTtsSpeechRate] = useState(1.2);

  const [testAsrStatus, setTestAsrStatus] = useState<Status>('READY');
  const [testAsrError, setTestAsrError] = useState('');
  const [testAsrPartial, setTestAsrPartial] = useState('');
  const [testAsrFinalLines, setTestAsrFinalLines] = useState<string[]>([]);
  const [testAsrLogs, setTestAsrLogs] = useState<string[]>([]);

  const [testTtsStatus, setTestTtsStatus] = useState<Status>('READY');
  const [testTtsError, setTestTtsError] = useState('');
  const [testTtsText, setTestTtsText] = useState('你好，欢迎来到统一语音服务控制台。');
  const [testTtsRenderedText, setTestTtsRenderedText] = useState('');
  const [testTtsLogs, setTestTtsLogs] = useState<string[]>([]);
  const [testTtsSampleRate, setTestTtsSampleRate] = useState(24000);
  const [testTtsChannels, setTestTtsChannels] = useState(1);

  const [qaStatus, setQaStatus] = useState<QaStatus>('IDLE');
  const [qaError, setQaError] = useState('');
  const [qaNotice, setQaNotice] = useState('');
  const [qaLatestUtterance, setQaLatestUtterance] = useState('');
  const [qaAssistantResponse, setQaAssistantResponse] = useState('');
  const [qaLogs, setQaLogs] = useState<string[]>([]);
  const [qaSessionActive, setQaSessionActive] = useState(false);
  const [qaChatId, setQaChatId] = useState('');
  const [qaSendPauseSeconds, setQaSendPauseSeconds] = useState(QA_SEND_PAUSE_DEFAULT_SECONDS);
  const [qaTtsSampleRate, setQaTtsSampleRate] = useState(24000);
  const [qaTtsChannels, setQaTtsChannels] = useState(1);
  const [qaMicPaused, setQaMicPaused] = useState(false);

  const playerRef = useRef(new PcmQueuePlayer());
  const readyCuePlayerRef = useRef(new ReadyCuePlayer());
  const pendingBinaryRef = useRef<PendingBinary[]>([]);
  const ttsAudioSummaryRef = useRef<Record<string, TtsAudioSummary>>({});
  const qaListeningTransitionRef = useRef(0);
  const qaPendingPlaybackChunksRef = useRef(0);
  const qaResumeAfterPlaybackPendingRef = useRef(false);
  const qaReadyCueDelayTimerRef = useRef<number | null>(null);
  const qaPlaybackDrainCheckInFlightRef = useRef(false);

  const qaSessionActiveRef = useRef(false);
  const qaAwaitingUserRef = useRef(false);
  const qaChatIdRef = useRef('');
  const qaAcceptChatUpdatesRef = useRef(true);
  const qaPendingUtteranceRef = useRef('');
  const qaSendTimerRef = useRef<number | null>(null);
  const captureOwnerRef = useRef<CaptureOwner>(null);
  const captureTaskIdRef = useRef<string | null>(null);
  const capturePausedRef = useRef(false);
  const asrClientGateConfigsRef = useRef<Record<string, ClientGateConfig>>({});

  const testAsrStatusRef = useRef<Status>('READY');
  const testTtsStatusRef = useRef<Status>('READY');
  const qaAsrStatusRef = useRef<Status>('READY');
  const qaTtsStatusRef = useRef<Status>('READY');
  const qaStatusRef = useRef<QaStatus>('IDLE');

  const asrStreamRef = useRef<MediaStream | null>(null);
  const asrAudioContextRef = useRef<AudioContext | null>(null);
  const asrSourceRef = useRef<MediaStreamAudioSourceNode | null>(null);
  const asrProcessorRef = useRef<ScriptProcessorNode | null>(null);
  const asrCaptureStartedRef = useRef(false);
  const asrRemainRef = useRef(new Uint8Array(0));
  const asrGateStateRef = useRef(createClientGateRuntime(DEFAULT_CLIENT_GATE));

  const asrAudioRefs: AudioCaptureRefs = {
    streamRef: asrStreamRef,
    audioContextRef: asrAudioContextRef,
    sourceRef: asrSourceRef,
    processorRef: asrProcessorRef,
    captureStartedRef: asrCaptureStartedRef,
    remainRef: asrRemainRef,
    gateStateRef: asrGateStateRef
  };

  const wsUrl = useMemo(() => resolveWsUrl(wsUrlInput), [wsUrlInput]);

  function resolveTaskClientGateConfig(overrides?: ClientGateSettings): ClientGateConfig {
    const base = normalizeClientGateConfig(capabilities?.asr?.defaults?.clientGate);
    if (overrides == null) {
      return base;
    }
    return normalizeClientGateConfig({ ...base, ...overrides });
  }

  function setTestAsrStatusValue(next: Status) {
    testAsrStatusRef.current = next;
    setTestAsrStatus(next);
  }

  function setTestTtsStatusValue(next: Status) {
    testTtsStatusRef.current = next;
    setTestTtsStatus(next);
  }

  function setQaAsrStatusValue(next: Status) {
    qaAsrStatusRef.current = next;
  }

  function setQaTtsStatusValue(next: Status) {
    qaTtsStatusRef.current = next;
  }

  function setQaStatusValue(next: QaStatus) {
    qaStatusRef.current = next;
    setQaStatus(next);
  }

  function isQaInErrorState() {
    return qaStatusRef.current === 'ERROR';
  }

  function setQaSessionActiveValue(next: boolean) {
    qaSessionActiveRef.current = next;
    setQaSessionActive(next);
  }

  function setQaChatIdValue(next: string) {
    qaChatIdRef.current = next;
    setQaChatId(next);
  }

  function appendLog(setter: Dispatch<SetStateAction<string[]>>, line: string) {
    const timestamped = `${new Date().toLocaleTimeString()} ${line}`;
    setter((prev) => [...prev, timestamped].slice(-200));
  }

  function appendTestAsrLog(line: string) {
    appendLog(setTestAsrLogs, line);
  }

  function appendTestTtsLog(line: string) {
    appendLog(setTestTtsLogs, line);
  }

  function appendQaLog(line: string) {
    appendLog(setQaLogs, line);
  }

  function cancelQaListeningTransition() {
    qaListeningTransitionRef.current += 1;
    readyCuePlayerRef.current.stop();
  }

  function clearQaReadyCueDelayTimer() {
    if (qaReadyCueDelayTimerRef.current != null) {
      window.clearTimeout(qaReadyCueDelayTimerRef.current);
      qaReadyCueDelayTimerRef.current = null;
    }
  }

  function cancelQaResumeAfterPlayback(options?: { resetPendingChunks?: boolean }) {
    qaResumeAfterPlaybackPendingRef.current = false;
    clearQaReadyCueDelayTimer();
    if (options?.resetPendingChunks) {
      qaPendingPlaybackChunksRef.current = 0;
    }
  }

  function resetTtsAudioSummary(taskId: string) {
    ttsAudioSummaryRef.current[taskId] = {
      chunks: 0,
      bytes: 0,
      startedAt: Date.now(),
      receivingLogged: false
    };
  }

  function trackTtsChunk(taskId: string, byteLength: number, onFirstChunk: () => void) {
    const current = ttsAudioSummaryRef.current[taskId] ?? {
      chunks: 0,
      bytes: 0,
      startedAt: Date.now(),
      receivingLogged: false
    };
    current.chunks += 1;
    current.bytes += byteLength;
    if (!current.receivingLogged) {
      current.receivingLogged = true;
      onFirstChunk();
    }
    ttsAudioSummaryRef.current[taskId] = current;
  }

  function flushTtsSummary(taskId: string, appendSummary: (line: string) => void) {
    const summary = ttsAudioSummaryRef.current[taskId];
    if (summary == null || summary.chunks === 0) {
      delete ttsAudioSummaryRef.current[taskId];
      return;
    }
    appendSummary(
      `audio complete: ${summary.chunks} chunks, ${formatByteSize(summary.bytes)}, ${formatDuration(Date.now() - summary.startedAt)}`
    );
    delete ttsAudioSummaryRef.current[taskId];
  }

  function resetTestTtsView() {
    setTestTtsError('');
    setTestTtsRenderedText('');
  }

  function resetQaConversationView() {
    setQaError('');
    setQaNotice('');
    setQaLatestUtterance('');
    setQaAssistantResponse('');
    setQaLogs([]);
  }

  function resetQaChatContext() {
    setQaChatIdValue('');
  }

  function clearQaSendTimer() {
    if (qaSendTimerRef.current != null) {
      window.clearTimeout(qaSendTimerRef.current);
      qaSendTimerRef.current = null;
    }
  }

  function clearQaPendingUtterance() {
    clearQaSendTimer();
    qaPendingUtteranceRef.current = '';
  }

  function flushQaPendingUtterance() {
    clearQaSendTimer();

    const mergedText = qaPendingUtteranceRef.current.trim();
    if (!mergedText) {
      qaPendingUtteranceRef.current = '';
      return;
    }
    if (!qaSessionActiveRef.current || !qaAwaitingUserRef.current || isTaskActive(qaTtsStatusRef.current)) {
      qaPendingUtteranceRef.current = '';
      return;
    }

    const normalizedLength = normalizeQaUtteranceForLength(mergedText).length;
    qaPendingUtteranceRef.current = '';

    if (normalizedLength < 2) {
      appendQaLog(`[ASR skipped] utterance too short (${normalizedLength} chars): ${mergedText}`);
      setQaStatusValue('LISTENING');
      return;
    }

    qaAwaitingUserRef.current = false;
    setQaAssistantResponse('');
    setQaStatusValue('THINKING');
    startQaTts(mergedText);
  }

  function scheduleQaPendingUtteranceFlush() {
    clearQaSendTimer();
    qaSendTimerRef.current = window.setTimeout(() => {
      qaSendTimerRef.current = null;
      flushQaPendingUtterance();
    }, qaSendPauseSeconds * 1000);
  }

  function resetAllAudioCaptureState() {
    cleanupAudioCapture(asrAudioRefs);
    captureOwnerRef.current = null;
    captureTaskIdRef.current = null;
    capturePausedRef.current = false;
    setQaMicPaused(false);
  }

  async function playQaReadyCue(context: 'initial' | 'resume') {
    try {
      await readyCuePlayerRef.current.playReadyCue();
    } catch (error) {
      appendQaLog(`[cue ${context}] failed: ${describeError(error)}`);
    }
  }

  function scheduleQaResumeAfterPlayback() {
    if (!qaResumeAfterPlaybackPendingRef.current || qaReadyCueDelayTimerRef.current != null) {
      return;
    }

    qaReadyCueDelayTimerRef.current = window.setTimeout(() => {
      qaReadyCueDelayTimerRef.current = null;
      if (
        !qaResumeAfterPlaybackPendingRef.current ||
        !qaSessionActiveRef.current ||
        isQaInErrorState() ||
        isTaskActive(qaTtsStatusRef.current) ||
        qaPendingPlaybackChunksRef.current > 0
      ) {
        return;
      }

      qaResumeAfterPlaybackPendingRef.current = false;
      void enterQaListeningReady({ resumeCapture: true });
    }, QA_READY_CUE_DELAY_MS);
  }

  async function checkQaPlaybackDrainAndResume() {
    if (qaPlaybackDrainCheckInFlightRef.current) {
      return;
    }
    qaPlaybackDrainCheckInFlightRef.current = true;

    try {
      if (
        !qaResumeAfterPlaybackPendingRef.current ||
        !qaSessionActiveRef.current ||
        isQaInErrorState() ||
        isTaskActive(qaTtsStatusRef.current) ||
        qaPendingPlaybackChunksRef.current > 0
      ) {
        return;
      }

      await playerRef.current.waitForIdle();

      if (
        !qaResumeAfterPlaybackPendingRef.current ||
        !qaSessionActiveRef.current ||
        isQaInErrorState() ||
        isTaskActive(qaTtsStatusRef.current) ||
        qaPendingPlaybackChunksRef.current > 0
      ) {
        return;
      }

      scheduleQaResumeAfterPlayback();
    } finally {
      qaPlaybackDrainCheckInFlightRef.current = false;
    }
  }

  async function enterQaListeningReady(options: { resumeCapture: boolean }) {
    const transitionId = qaListeningTransitionRef.current + 1;
    qaListeningTransitionRef.current = transitionId;

    clearQaPendingUtterance();
    qaAwaitingUserRef.current = false;

    if (options.resumeCapture) {
      await playerRef.current.waitForIdle();
    }

    if (qaListeningTransitionRef.current !== transitionId || !qaSessionActiveRef.current || isQaInErrorState()) {
      return;
    }

    await playQaReadyCue(options.resumeCapture ? 'resume' : 'initial');

    if (qaListeningTransitionRef.current !== transitionId || !qaSessionActiveRef.current || isQaInErrorState()) {
      return;
    }

    const captureReady = options.resumeCapture
      ? await resumeQaAudioCapture()
      : await ensureAudioCaptureFor(QA_ASR_TASK_ID, 'qa');

    if (!captureReady) {
      return;
    }

    if (qaListeningTransitionRef.current !== transitionId || !qaSessionActiveRef.current || isQaInErrorState()) {
      return;
    }

    clearQaPendingUtterance();
    qaAwaitingUserRef.current = true;
    setQaStatusValue('LISTENING');
    setQaNotice('');
  }

  const { status: connectionStatus, sendJson, reconnect } = useVoiceSocket(wsUrl, {
    onJsonMessage: (rawMessage) => {
      const message = rawMessage as ServerEvent;

      if (message.taskId === TEST_ASR_TASK_ID) {
        appendTestAsrLog(`[${message.type}] ${message.text ?? message.reason ?? message.message ?? ''}`.trim());

        if (message.type === 'task.started') {
          void ensureAudioCaptureFor(TEST_ASR_TASK_ID, 'test');
          return;
        }
        if (message.type === 'asr.text.delta' && message.text) {
          setTestAsrPartial((prev) => `${prev}${message.text}`);
          return;
        }
        if (message.type === 'asr.text.final' && message.text) {
          const finalText = message.text;
          setTestAsrFinalLines((prev) => [...prev, finalText]);
          setTestAsrPartial('');
          return;
        }
        if (message.type === 'error') {
          delete asrClientGateConfigsRef.current[TEST_ASR_TASK_ID];
          setTestAsrError(`${message.code ?? 'ERROR'}: ${message.message ?? 'unknown error'}`);
          setTestAsrStatusValue('ERROR');
          if (captureTaskIdRef.current === TEST_ASR_TASK_ID) {
            resetAllAudioCaptureState();
          }
          return;
        }
        if (message.type === 'task.stopped') {
          delete asrClientGateConfigsRef.current[TEST_ASR_TASK_ID];
          if (captureTaskIdRef.current === TEST_ASR_TASK_ID) {
            resetAllAudioCaptureState();
          }
          if (testAsrStatusRef.current !== 'ERROR') {
            setTestAsrStatusValue('STOPPED');
          }
          return;
        }
      }

      if (message.taskId === QA_ASR_TASK_ID) {
        if (message.type !== 'asr.text.delta') {
          appendQaLog(`[ASR ${message.type}] ${message.text ?? message.reason ?? message.message ?? ''}`.trim());
        }

        if (message.type === 'task.started') {
          setQaAsrStatusValue('STREAMING');
          if (qaSessionActiveRef.current && qaStatusRef.current === 'STARTING') {
            void enterQaListeningReady({ resumeCapture: false });
          } else if (!capturePausedRef.current) {
            void ensureAudioCaptureFor(QA_ASR_TASK_ID, 'qa');
          } else {
            setQaStatusValue('SPEAKING');
          }
          return;
        }
        if (message.type === 'asr.text.final' && message.text) {
          if (qaSessionActiveRef.current && qaAwaitingUserRef.current && !isTaskActive(qaTtsStatusRef.current)) {
            const mergedText = mergeQaUtterance(qaPendingUtteranceRef.current, message.text);
            qaPendingUtteranceRef.current = mergedText;
            setQaLatestUtterance(mergedText);
            scheduleQaPendingUtteranceFlush();
          }
          return;
        }
        if (message.type === 'error') {
          delete asrClientGateConfigsRef.current[QA_ASR_TASK_ID];
          cancelQaListeningTransition();
          clearQaPendingUtterance();
          resetQaChatContext();
          setQaError(`${message.code ?? 'ERROR'}: ${message.message ?? 'unknown error'}`);
          setQaStatusValue('ERROR');
          setQaSessionActiveValue(false);
          qaAwaitingUserRef.current = false;
          setQaAsrStatusValue('ERROR');
          if (captureTaskIdRef.current === QA_ASR_TASK_ID) {
            resetAllAudioCaptureState();
          }
          return;
        }
        if (message.type === 'task.stopped') {
          delete asrClientGateConfigsRef.current[QA_ASR_TASK_ID];
          cancelQaListeningTransition();
          clearQaPendingUtterance();
          resetQaChatContext();
          if (captureTaskIdRef.current === QA_ASR_TASK_ID) {
            resetAllAudioCaptureState();
          }
          setQaAsrStatusValue('STOPPED');
          qaAwaitingUserRef.current = false;
          if (qaSessionActiveRef.current) {
            setQaSessionActiveValue(false);
            setQaNotice('QA 会话已停止。');
          }
          if (qaStatusRef.current !== 'ERROR') {
            setQaStatusValue('IDLE');
          }
        }
        return;
      }

      if (message.taskId === TEST_TTS_TASK_ID) {
        if (message.type !== 'tts.audio.chunk') {
          appendTestTtsLog(`[${message.type}] ${message.text ?? message.reason ?? message.message ?? ''}`.trim());
        }

        if (message.type === 'task.started') {
          setTestTtsStatusValue('STREAMING');
          setQaTtsStatusValue('READY');
          pendingBinaryRef.current = [];
          resetTtsAudioSummary(TEST_TTS_TASK_ID);
          return;
        }
        if (message.type === 'tts.audio.format') {
          if (typeof message.sampleRate === 'number') {
            setTestTtsSampleRate(message.sampleRate);
          }
          if (typeof message.channels === 'number') {
            setTestTtsChannels(message.channels);
          }
          return;
        }
        if (message.type === 'tts.audio.chunk') {
          pendingBinaryRef.current.push({ taskId: TEST_TTS_TASK_ID, seq: message.seq ?? 0 });
          trackTtsChunk(TEST_TTS_TASK_ID, message.byteLength ?? 0, () => appendTestTtsLog('receiving audio...'));
          return;
        }
        if (message.type === 'tts.done' && testTtsStatusRef.current !== 'ERROR') {
          setTestTtsStatusValue('STOPPED');
          return;
        }
        if (message.type === 'error') {
          setTestTtsError(`${message.code ?? 'ERROR'}: ${message.message ?? 'unknown error'}`);
          setTestTtsStatusValue('ERROR');
          flushTtsSummary(TEST_TTS_TASK_ID, appendTestTtsLog);
          return;
        }
        if (message.type === 'task.stopped') {
          if (testTtsStatusRef.current !== 'ERROR') {
            setTestTtsStatusValue('STOPPED');
          }
          flushTtsSummary(TEST_TTS_TASK_ID, appendTestTtsLog);
        }
        return;
      }

      if (message.taskId === QA_TTS_TASK_ID) {
        if (message.type !== 'tts.audio.chunk') {
          appendQaLog(`[TTS ${message.type}] ${message.text ?? message.reason ?? message.message ?? ''}`.trim());
        }

        if (message.type === 'task.started') {
          cancelQaListeningTransition();
          cancelQaResumeAfterPlayback({ resetPendingChunks: true });
          clearQaPendingUtterance();
          setQaTtsStatusValue('STREAMING');
          pendingBinaryRef.current = [];
          resetTtsAudioSummary(QA_TTS_TASK_ID);
          pauseQaAudioCapture();
          setQaStatusValue('SPEAKING');
          return;
        }
        if (message.type === 'tts.audio.format') {
          if (typeof message.sampleRate === 'number') {
            setQaTtsSampleRate(message.sampleRate);
          }
          if (typeof message.channels === 'number') {
            setQaTtsChannels(message.channels);
          }
          return;
        }
        if (message.type === 'tts.audio.chunk') {
          qaPendingPlaybackChunksRef.current += 1;
          pendingBinaryRef.current.push({ taskId: QA_TTS_TASK_ID, seq: message.seq ?? 0 });
          trackTtsChunk(QA_TTS_TASK_ID, message.byteLength ?? 0, () => appendQaLog('Receiving audio...'));
          return;
        }
        if (message.type === 'tts.text.delta' && message.text) {
          setQaAssistantResponse((prev) => `${prev}${message.text}`);
          return;
        }
        if (message.type === 'tts.chat.updated' && message.chatId) {
          if (!qaAcceptChatUpdatesRef.current) {
            return;
          }
          setQaChatIdValue(message.chatId);
          return;
        }
        if (message.type === 'tts.done' && qaTtsStatusRef.current !== 'ERROR') {
          setQaTtsStatusValue('STOPPED');
          return;
        }
        if (message.type === 'error') {
          cancelQaListeningTransition();
          cancelQaResumeAfterPlayback({ resetPendingChunks: true });
          clearQaPendingUtterance();
          resetQaChatContext();
          setQaError(`${message.code ?? 'ERROR'}: ${message.message ?? 'unknown error'}`);
          setQaStatusValue('ERROR');
          setQaTtsStatusValue('ERROR');
          setQaSessionActiveValue(false);
          qaAwaitingUserRef.current = false;
          flushTtsSummary(QA_TTS_TASK_ID, appendQaLog);
          return;
        }
        if (message.type === 'task.stopped') {
          flushTtsSummary(QA_TTS_TASK_ID, appendQaLog);
          if (qaTtsStatusRef.current !== 'ERROR') {
            setQaTtsStatusValue('STOPPED');
          }
          if (qaSessionActiveRef.current) {
            qaResumeAfterPlaybackPendingRef.current = true;
            void checkQaPlaybackDrainAndResume();
          }
        }
      }
    },
    onBinaryMessage: async (buffer) => {
      const pending = pendingBinaryRef.current.shift();
      if (pending == null) {
        return;
      }

      if (pending.taskId === TEST_TTS_TASK_ID) {
        await playerRef.current.enqueue(buffer, testTtsSampleRate, testTtsChannels);
        return;
      }

      if (pending.taskId === QA_TTS_TASK_ID) {
        try {
          await playerRef.current.enqueue(buffer, qaTtsSampleRate, qaTtsChannels);
        } finally {
          qaPendingPlaybackChunksRef.current = Math.max(0, qaPendingPlaybackChunksRef.current - 1);
        }
        void checkQaPlaybackDrainAndResume();
      }
    },
    onOpen: () => {
      appendTestAsrLog(`connection open: ${wsUrl}`);
      appendTestTtsLog(`connection open: ${wsUrl}`);
      appendQaLog(`connection open: ${wsUrl}`);
    },
    onClose: () => {
      cancelQaListeningTransition();
      cancelQaResumeAfterPlayback({ resetPendingChunks: true });
      clearQaPendingUtterance();
      resetQaChatContext();
      resetAllAudioCaptureState();
      asrClientGateConfigsRef.current = {};
      pendingBinaryRef.current = [];
      playerRef.current.stopAll();
      setQaSessionActiveValue(false);
      qaAwaitingUserRef.current = false;

      if (isTaskActive(testAsrStatusRef.current)) {
        setTestAsrStatusValue('STOPPED');
      }
      if (isTaskActive(testTtsStatusRef.current)) {
        setTestTtsStatusValue('STOPPED');
      }
      if (isTaskActive(qaTtsStatusRef.current) || isTaskActive(qaAsrStatusRef.current)) {
        setQaStatusValue('IDLE');
      }
      setQaAsrStatusValue('STOPPED');
      setQaTtsStatusValue('STOPPED');
    },
    onError: () => {
      setTestAsrError((prev) => prev || 'WebSocket 连接异常');
      setTestTtsError((prev) => prev || 'WebSocket 连接异常');
      setQaError((prev) => prev || 'WebSocket 连接异常');
    }
  });

  async function ensureAudioCaptureFor(taskId: string, owner: Exclude<CaptureOwner, null>): Promise<boolean> {
    if (capturePausedRef.current) {
      return false;
    }
    captureOwnerRef.current = owner;
    captureTaskIdRef.current = taskId;

    const clientGateConfig = asrClientGateConfigsRef.current[taskId] ?? resolveTaskClientGateConfig();
    resetClientGateRuntime(asrAudioRefs.gateStateRef.current, clientGateConfig);

    if (asrCaptureStartedRef.current) {
      return true;
    }

    let started = false;

    await initializeAudioCapture(
      asrAudioRefs,
      (chunk) => {
        sendJson({
          type: 'asr.audio.append',
          taskId,
          audio: bytesToBase64(chunk)
        });
      },
      () => {
        started = true;
        if (owner === 'test') {
          setTestAsrStatusValue('STREAMING');
          return;
        }
        setQaAsrStatusValue('STREAMING');
      },
      (message) => {
        if (owner === 'test') {
          setTestAsrError(message);
          setTestAsrStatusValue('ERROR');
          return;
        }
        clearQaPendingUtterance();
        resetQaChatContext();
        setQaError(message);
        setQaStatusValue('ERROR');
        setQaSessionActiveValue(false);
        qaAwaitingUserRef.current = false;
      }
    );

    return started || asrCaptureStartedRef.current;
  }

  function pauseQaAudioCapture() {
    if (captureOwnerRef.current !== 'qa' || captureTaskIdRef.current !== QA_ASR_TASK_ID || capturePausedRef.current) {
      return;
    }
    cleanupAudioCapture(asrAudioRefs);
    capturePausedRef.current = true;
    setQaMicPaused(true);
  }

  async function resumeQaAudioCapture(): Promise<boolean> {
    if (!capturePausedRef.current) {
      return asrCaptureStartedRef.current;
    }
    capturePausedRef.current = false;
    setQaMicPaused(false);
    if (!qaSessionActiveRef.current || !isTaskActive(qaAsrStatusRef.current)) {
      return false;
    }
    return ensureAudioCaptureFor(QA_ASR_TASK_ID, 'qa');
  }

  function stopAsrTask(taskId: string) {
    if (captureTaskIdRef.current === taskId && asrRemainRef.current.length > 0) {
      sendJson({
        type: 'asr.audio.append',
        taskId,
        audio: bytesToBase64(asrRemainRef.current)
      });
    }
    sendJson({ type: 'asr.audio.commit', taskId });
    sendJson({ type: 'asr.stop', taskId });
    delete asrClientGateConfigsRef.current[taskId];
    if (captureTaskIdRef.current === taskId) {
      resetAllAudioCaptureState();
    }
  }

  function startTestAsr() {
    if (connectionStatus !== 'OPEN') {
      setTestAsrError('WebSocket 尚未连接，请先等待连接建立或手动重连。');
      return;
    }

    setTestAsrError('');
    setTestAsrPartial('');
    setTestAsrFinalLines([]);
    setTestAsrLogs([]);
    if (captureTaskIdRef.current === TEST_ASR_TASK_ID) {
      resetAllAudioCaptureState();
    }

    setTestAsrStatusValue('CONNECTING');
    const clientGate = resolveTaskClientGateConfig();
    asrClientGateConfigsRef.current[TEST_ASR_TASK_ID] = clientGate;
    sendJson({
      type: 'asr.start',
      taskId: TEST_ASR_TASK_ID,
      sampleRate: capabilities?.asr?.defaults?.sampleRate ?? 16000,
      language: capabilities?.asr?.defaults?.language ?? 'zh',
      clientGate,
      turnDetection: {
        type: capabilities?.asr?.defaults?.turnDetection?.type ?? 'server_vad',
        threshold: capabilities?.asr?.defaults?.turnDetection?.threshold ?? 0,
        silenceDurationMs: capabilities?.asr?.defaults?.turnDetection?.silenceDurationMs ?? 400
      }
    });
  }

  function stopTestAsr() {
    stopAsrTask(TEST_ASR_TASK_ID);
    setTestAsrStatusValue('STOPPED');
  }

  function startTestTts() {
    if (connectionStatus !== 'OPEN') {
      setTestTtsError('WebSocket 尚未连接，请先等待连接建立或手动重连。');
      return;
    }
    if (testTtsText.trim().length === 0) {
      setTestTtsError('请输入要测试的文本。');
      return;
    }
    if (!voiceSelectionReady) {
      setTestTtsError(voiceUnavailableMessage);
      return;
    }

    resetTestTtsView();
    setTestTtsLogs([]);
    setTestTtsStatusValue('CONNECTING');
    pendingBinaryRef.current = pendingBinaryRef.current.filter((item) => item.taskId !== TEST_TTS_TASK_ID);
    playerRef.current.stopAll();

    const sent = sendJson({
      type: 'tts.start',
      taskId: TEST_TTS_TASK_ID,
      mode: 'local',
      text: testTtsText,
      voice: selectedVoice,
      speechRate: ttsSpeechRate
    });
    if (!sent) {
      setTestTtsError('WebSocket 尚未连接，请先等待连接建立或手动重连。');
      setTestTtsStatusValue('ERROR');
    }
  }

  function stopTestTts() {
    sendJson({ type: 'tts.stop', taskId: TEST_TTS_TASK_ID });
    pendingBinaryRef.current = pendingBinaryRef.current.filter((item) => item.taskId !== TEST_TTS_TASK_ID);
    playerRef.current.stopAll();
    setTestTtsStatusValue('STOPPED');
  }

  function startQaTts(text: string) {
    if (!voiceSelectionReady) {
      setQaError(voiceUnavailableMessage);
      setQaStatusValue('ERROR');
      setQaSessionActiveValue(false);
      qaAwaitingUserRef.current = false;
      return;
    }
    qaAcceptChatUpdatesRef.current = true;
    setQaTtsStatusValue('CONNECTING');
    pendingBinaryRef.current = pendingBinaryRef.current.filter((item) => item.taskId !== QA_TTS_TASK_ID);
    playerRef.current.resetQueue();

    const payload: Record<string, unknown> = {
      type: 'tts.start',
      taskId: QA_TTS_TASK_ID,
      mode: 'llm',
      text,
      voice: selectedVoice,
      speechRate: ttsSpeechRate
    };
    if (qaChatIdRef.current) {
      payload.chatId = qaChatIdRef.current;
    }

    const sent = sendJson(payload);
    if (!sent) {
      setQaError('WebSocket 尚未连接，请先等待连接建立或手动重连。');
      setQaStatusValue('ERROR');
      setQaSessionActiveValue(false);
      qaAwaitingUserRef.current = false;
    }
  }

  function startQa() {
    if (connectionStatus !== 'OPEN') {
      setQaError('WebSocket 尚未连接，请先等待连接建立或手动重连。');
      return;
    }
    if (capabilities?.tts?.runnerConfigured === false) {
      setQaError('当前后端未配置 runner，QA 模式不可用。');
      return;
    }
    if (!voiceSelectionReady) {
      setQaError(voiceUnavailableMessage);
      return;
    }

    cancelQaListeningTransition();
    cancelQaResumeAfterPlayback({ resetPendingChunks: true });
    resetQaConversationView();
    resetQaChatContext();
    qaAcceptChatUpdatesRef.current = true;
    clearQaPendingUtterance();
    void readyCuePlayerRef.current.prime().catch((error) => {
      appendQaLog(`[cue prime] failed: ${describeError(error)}`);
    });
    setQaStatusValue('STARTING');
    setQaSessionActiveValue(true);
    qaAwaitingUserRef.current = false;
    setQaAsrStatusValue('CONNECTING');
    setQaTtsStatusValue('READY');
    const clientGate = resolveTaskClientGateConfig();
    asrClientGateConfigsRef.current[QA_ASR_TASK_ID] = clientGate;

    const sent = sendJson({
      type: 'asr.start',
      taskId: QA_ASR_TASK_ID,
      sampleRate: capabilities?.asr?.defaults?.sampleRate ?? 16000,
      language: capabilities?.asr?.defaults?.language ?? 'zh',
      clientGate,
      turnDetection: {
        type: capabilities?.asr?.defaults?.turnDetection?.type ?? 'server_vad',
        threshold: capabilities?.asr?.defaults?.turnDetection?.threshold ?? 0,
        silenceDurationMs: capabilities?.asr?.defaults?.turnDetection?.silenceDurationMs ?? 400
      }
    });
    if (!sent) {
      setQaError('WebSocket 尚未连接，请先等待连接建立或手动重连。');
      setQaStatusValue('ERROR');
      setQaSessionActiveValue(false);
      qaAwaitingUserRef.current = false;
    }
  }

  function stopQa() {
    cancelQaListeningTransition();
    cancelQaResumeAfterPlayback({ resetPendingChunks: true });
    setQaSessionActiveValue(false);
    qaAwaitingUserRef.current = false;
    setQaNotice('');
    qaAcceptChatUpdatesRef.current = true;
    resetQaChatContext();
    clearQaPendingUtterance();

    if (isTaskActive(qaTtsStatusRef.current)) {
      sendJson({ type: 'tts.stop', taskId: QA_TTS_TASK_ID });
    }
    pendingBinaryRef.current = pendingBinaryRef.current.filter((item) => item.taskId !== QA_TTS_TASK_ID);
    playerRef.current.stopAll();
    flushTtsSummary(QA_TTS_TASK_ID, appendQaLog);
    setQaTtsStatusValue('STOPPED');

    if (isTaskActive(qaAsrStatusRef.current)) {
      stopAsrTask(QA_ASR_TASK_ID);
    } else if (captureTaskIdRef.current === QA_ASR_TASK_ID) {
      resetAllAudioCaptureState();
    }
    setQaAsrStatusValue('STOPPED');
    setQaStatusValue('IDLE');
  }

  function startNewQaChat() {
    qaAcceptChatUpdatesRef.current = false;
    resetQaChatContext();
    appendQaLog('[QA] started a new chat context');
  }

  function handleModeChange(nextMode: ViewMode) {
    if (nextMode === viewMode) {
      return;
    }

    if (viewMode === 'qa' && (qaSessionActiveRef.current || isTaskActive(qaAsrStatusRef.current) || isTaskActive(qaTtsStatusRef.current))) {
      stopQa();
    }
    if (viewMode === 'test') {
      if (isTaskActive(testAsrStatusRef.current)) {
        stopTestAsr();
      }
      if (isTaskActive(testTtsStatusRef.current)) {
        stopTestTts();
      }
    }

    setViewMode(nextMode);
  }

  useEffect(() => {
    fetch('/api/voice/capabilities')
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`);
        }
        return response.json() as Promise<CapabilitiesResponse>;
      })
      .then((data) => {
        setCapabilities(data);
        if (typeof data.tts?.audioFormat?.sampleRate === 'number') {
          setTestTtsSampleRate(data.tts.audioFormat.sampleRate);
          setQaTtsSampleRate(data.tts.audioFormat.sampleRate);
        }
        if (typeof data.tts?.audioFormat?.channels === 'number') {
          setTestTtsChannels(data.tts.audioFormat.channels);
          setQaTtsChannels(data.tts.audioFormat.channels);
        }
        if (typeof data.tts?.speechRateDefault === 'number') {
          setTtsSpeechRate(clampSpeechRate(data.tts.speechRateDefault));
        }
        if (typeof data.websocketPath === 'string' && !isLocalDevHost(window.location.hostname)) {
          const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
          setWsUrlInput((current) => current.trim() || `${protocol}//${window.location.host}${data.websocketPath}`);
        }
      })
      .catch((error) => {
        setTestAsrError(`能力接口加载失败: ${error instanceof Error ? error.message : String(error)}`);
      });

    fetch('/api/voice/tts/voices')
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`);
        }
        return response.json();
      })
      .then((data) => {
        const nextVoices = Array.isArray(data.voices) ? (data.voices as VoiceOption[]) : [];
        setVoices(nextVoices);
        const nextDefaultVoice =
          typeof data.defaultVoice === 'string' && data.defaultVoice
            ? data.defaultVoice
            : nextVoices.find((voice) => voice.default)?.id ?? nextVoices[0]?.id ?? '';
        setSelectedVoice(nextDefaultVoice);
        setVoicesError(nextVoices.length === 0 ? '当前没有可用音色配置。' : '');
        setVoicesLoaded(true);
      })
      .catch((error) => {
        setVoices([]);
        setSelectedVoice('');
        setVoicesError(`音色列表加载失败: ${error instanceof Error ? error.message : String(error)}`);
        setVoicesLoaded(true);
      });
  }, []);

  useEffect(() => {
    return () => {
      clearQaPendingUtterance();
      resetAllAudioCaptureState();
      playerRef.current.stopAll();
    };
  }, []);

  const voiceSelectionReady = voices.length > 0 && selectedVoice.trim().length > 0;
  const noVoicesConfigured = voicesLoaded && !voiceSelectionReady;
  const voiceUnavailableMessage = voicesError || '当前没有可用音色配置。';
  const runnerUnavailable = capabilities?.tts?.runnerConfigured === false;
  const qaModeUnavailable = runnerUnavailable || noVoicesConfigured;
  const qaStartDisabled = runnerUnavailable || !voiceSelectionReady || qaSessionActive || qaStatus === 'STARTING' || qaStatus === 'THINKING' || qaStatus === 'SPEAKING';
  const qaStopDisabled = !qaSessionActive && !isTaskActive(qaAsrStatusRef.current) && !isTaskActive(qaTtsStatusRef.current);
  const qaStatusText =
    qaStatus === 'IDLE'
      ? '空闲'
      : qaStatus === 'STARTING'
        ? '正在启动语音会话...'
        : qaStatus === 'LISTENING'
          ? '等待用户说话...'
          : qaStatus === 'THINKING'
            ? 'LLM 正在生成回答...'
            : qaStatus === 'SPEAKING'
              ? '正在播放回答，麦克风已暂停'
              : 'QA 会话异常';

  return (
    <div className="shell">
      <div className="backdrop" />
      <div className="frame">
        <section className="hero">
          <div>
            <p className="eyebrow">Voice Protocol v2</p>
            <h1>Testing and QA, clearly separated.</h1>
            <p className="hero-copy">
              测试模式专注独立验证 ASR 与本地 TTS，QA 模式专注完整的 ASR - LLM - TTS 闭环。
            </p>
          </div>
          <div className="ws-card">
            <label htmlFor="ws-url">WebSocket URL</label>
            <input id="ws-url" value={wsUrlInput} onChange={(event) => setWsUrlInput(event.target.value)} />
            <div className="ws-controls">
              <div className="status-row ws-toolbar">
                <span className={statusClass(connectionStatus)}>{connectionStatus}</span>
                <button className="ghost" onClick={reconnect}>
                  Reconnect
                </button>
              </div>
              <div className="mode-switch" role="tablist" aria-label="View mode">
                <button
                  className={viewMode === 'test' ? 'mode-tab active' : 'mode-tab'}
                  onClick={() => handleModeChange('test')}
                  type="button"
                >
                  Test Mode
                </button>
                <button
                  className={viewMode === 'qa' ? 'mode-tab active' : 'mode-tab'}
                  onClick={() => handleModeChange('qa')}
                  type="button"
                >
                  QA Mode
                </button>
              </div>
            </div>
          </div>
        </section>

        {viewMode === 'test' ? (
          <section className="panel-grid">
            <article className="panel">
              <div className="panel-head">
                <div>
                  <h2>ASR Test</h2>
                  <p className="panel-copy">手动验证实时识别链路，只关注麦克风到识别结果。</p>
                </div>
                <span className={statusClass(testAsrStatus)}>{testAsrStatus}</span>
              </div>

              <div className="actions">
                <button className="primary" onClick={startTestAsr} disabled={isTaskActive(testAsrStatus)}>
                  Start ASR
                </button>
                <button className="ghost" onClick={stopTestAsr} disabled={!isTaskActive(testAsrStatus)}>
                  Stop ASR
                </button>
              </div>

              {testAsrError ? <p className="error-text">{testAsrError}</p> : null}

              <div className="result-card">
                <div className="result-label">Partial</div>
                <div className="result-text">{testAsrPartial || '等待语音输入...'}</div>
              </div>

              <div className="result-card">
                <div className="result-label">Final</div>
                <div className="final-list">
                  {testAsrFinalLines.length === 0 ? <div className="muted">识别结果会追加在这里。</div> : null}
                  {testAsrFinalLines.map((line, index) => (
                    <div key={`${line}-${index}`}>{line}</div>
                  ))}
                </div>
              </div>

              <div className="log-card">
                <div className="log-head">
                  <h3>ASR Logs</h3>
                  <button className="mini" onClick={() => setTestAsrLogs([])}>
                    Clear
                  </button>
                </div>
                <pre>{testAsrLogs.join('\n') || '暂无日志。'}</pre>
              </div>
            </article>

            <article className="panel">
              <div className="panel-head">
                <div>
                  <h2>TTS Test</h2>
                  <p className="panel-copy">独立验证纯文本本地 TTS 播放，不受 QA 会话状态影响。</p>
                </div>
                <span className={statusClass(testTtsStatus)}>{testTtsStatus}</span>
              </div>

              <div className="field-grid">
                <label>
                  <span>Voice</span>
                  <select value={selectedVoice} onChange={(event) => setSelectedVoice(event.target.value)} disabled={voices.length === 0}>
                    {voices.length === 0 ? <option value="">No voices available</option> : null}
                    {voices.map((voice) => (
                      <option key={voice.id} value={voice.id}>
                        {voice.displayName}
                      </option>
                    ))}
                  </select>
                </label>

                <label>
                  <span>Speech Rate</span>
                  <input
                    type="number"
                    min="0.5"
                    max="2.0"
                    step="0.1"
                    value={ttsSpeechRate}
                    onChange={(event) => setTtsSpeechRate(clampSpeechRate(Number(event.target.value)))}
                  />
                </label>

              </div>

              <label className="stacked-field">
                <span>Input</span>
                <textarea value={testTtsText} onChange={(event) => setTestTtsText(event.target.value)} rows={8} />
              </label>

              <div className="actions">
                <button
                  className="primary"
                  onClick={startTestTts}
                  disabled={!voiceSelectionReady || testTtsStatus === 'CONNECTING' || testTtsStatus === 'STREAMING'}
                >
                  Start TTS
                </button>
                <button className="ghost" onClick={stopTestTts} disabled={!isTaskActive(testTtsStatus)}>
                  Stop TTS
                </button>
              </div>

              {testTtsError ? <p className="error-text">{testTtsError}</p> : null}
              {!testTtsError && noVoicesConfigured ? <p className="error-text">{voiceUnavailableMessage}</p> : null}

              <div className="result-card">
                <div className="result-label">Rendered Text</div>
                <div className="result-text">{testTtsRenderedText || testTtsText}</div>
              </div>

              <div className="meta-row">
                <span>SampleRate: {testTtsSampleRate}</span>
                <span>Channels: {testTtsChannels}</span>
                <span>SpeechRate: {ttsSpeechRate.toFixed(1)}</span>
              </div>

              <div className="log-card">
                <div className="log-head">
                  <h3>TTS Logs</h3>
                  <button className="mini" onClick={() => setTestTtsLogs([])}>
                    Clear
                  </button>
                </div>
                <pre>{testTtsLogs.join('\n') || '暂无日志。'}</pre>
              </div>
            </article>
          </section>
        ) : (
          <section className="qa-layout">
            <article className="panel qa-panel">
              <div className="panel-head">
                <div>
                  <h2>QA Mode</h2>
                  <p className="panel-copy">单按钮启动完整会话：ASR -&gt; LLM -&gt; TTS -&gt; 等待下一轮用户输入。</p>
                </div>
                <span className={qaStatusClass(qaStatus)}>{qaStatus}</span>
              </div>

              <div className="field-grid">
                <label>
                  <span>Voice</span>
                  <select value={selectedVoice} onChange={(event) => setSelectedVoice(event.target.value)} disabled={voices.length === 0}>
                    {voices.length === 0 ? <option value="">No voices available</option> : null}
                    {voices.map((voice) => (
                      <option key={voice.id} value={voice.id}>
                        {voice.displayName}
                      </option>
                    ))}
                  </select>
                </label>

                <label>
                  <span>Speech Rate</span>
                  <input
                    type="number"
                    min="0.5"
                    max="2.0"
                    step="0.1"
                    value={ttsSpeechRate}
                    onChange={(event) => setTtsSpeechRate(clampSpeechRate(Number(event.target.value)))}
                  />
                </label>

                <label>
                  <span>Send Pause (s)</span>
                  <input
                    type="number"
                    min={QA_SEND_PAUSE_MIN_SECONDS}
                    max={QA_SEND_PAUSE_MAX_SECONDS}
                    step="0.1"
                    value={qaSendPauseSeconds}
                    onChange={(event) => setQaSendPauseSeconds(clampQaSendPauseSeconds(Number(event.target.value)))}
                  />
                </label>
              </div>

              <div className="actions qa-actions">
                <div className="qa-primary-actions">
                  <button className="primary" onClick={startQa} disabled={qaStartDisabled}>
                    Start QA
                  </button>
                  <button className="ghost" onClick={stopQa} disabled={qaStopDisabled}>
                    Stop QA
                  </button>
                </div>
                <div className="qa-chat-inline">
                  <button className="ghost" onClick={startNewQaChat} disabled={!qaSessionActive && !qaChatId}>
                    New Chat
                  </button>
                  <span className="qa-chat-id" title={qaChatId || '新会话'}>
                    {qaChatId || '新会话'}
                  </span>
                </div>
              </div>

              {runnerUnavailable ? <p className="warn-text">当前后端未配置 runner，QA 模式不可用。</p> : null}
              {qaNotice ? <p className="warn-text">{qaNotice}</p> : null}
              {qaError ? <p className="error-text">{qaError}</p> : null}
              {!qaError && noVoicesConfigured ? <p className="error-text">{voiceUnavailableMessage}</p> : null}

              <div className="qa-summary-row">
                <div className="result-card qa-summary-card">
                  <div className="result-label">QA Status</div>
                  <div className="result-text">{qaStatusText}</div>
                </div>

                <div className="result-card qa-summary-card">
                  <div className="result-label">Microphone</div>
                  <div className="result-text">{qaMicPaused ? 'TTS 播放期间已暂停采集。' : '麦克风处于可采集状态。'}</div>
                </div>
              </div>

              <div className="qa-grid">
                <div className="result-card">
                  <div className="result-label">Latest User Utterance</div>
                  <div className="result-text">{qaLatestUtterance || '等待用户说话...'}</div>
                </div>

                <div className="result-card">
                  <div className="result-label">Assistant Response</div>
                  <div className="result-text">{qaAssistantResponse || '等待 LLM 返回回答...'}</div>
                </div>
              </div>

              <div className="meta-row">
                <span>SampleRate: {qaTtsSampleRate}</span>
                <span>Channels: {qaTtsChannels}</span>
                <span>SpeechRate: {ttsSpeechRate.toFixed(1)}</span>
                <span>SendPause: {qaSendPauseSeconds.toFixed(1)}s</span>
              </div>

              <div className="log-card">
                <div className="log-head">
                  <h3>QA Logs</h3>
                  <button className="mini" onClick={() => setQaLogs([])}>
                    Clear
                  </button>
                </div>
                <pre>{qaLogs.join('\n') || '暂无日志。'}</pre>
              </div>
            </article>
          </section>
        )}
      </div>
    </div>
  );
}
