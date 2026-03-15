export function downsampleBuffer(input: Float32Array, inputSampleRate: number, outputSampleRate = 16000): Float32Array {
  if (outputSampleRate === inputSampleRate) {
    return input;
  }

  if (outputSampleRate > inputSampleRate) {
    throw new Error('Output sample rate should be lower than input sample rate.');
  }

  const sampleRateRatio = inputSampleRate / outputSampleRate;
  const newLength = Math.round(input.length / sampleRateRatio);
  const result = new Float32Array(newLength);

  let offsetResult = 0;
  let offsetBuffer = 0;
  while (offsetResult < result.length) {
    const nextOffsetBuffer = Math.round((offsetResult + 1) * sampleRateRatio);
    let accum = 0;
    let count = 0;

    for (let i = offsetBuffer; i < nextOffsetBuffer && i < input.length; i += 1) {
      accum += input[i];
      count += 1;
    }

    result[offsetResult] = count > 0 ? accum / count : 0;
    offsetResult += 1;
    offsetBuffer = nextOffsetBuffer;
  }

  return result;
}

export function floatTo16BitPCM(float32Array: Float32Array): Int16Array {
  const output = new Int16Array(float32Array.length);

  for (let i = 0; i < float32Array.length; i += 1) {
    const sample = Math.max(-1, Math.min(1, float32Array[i]));
    output[i] = sample < 0 ? sample * 0x8000 : sample * 0x7fff;
  }

  return output;
}

export function encodePcm16(float32Array: Float32Array, inputSampleRate: number, targetSampleRate = 16000): Int16Array {
  const downsampled = downsampleBuffer(float32Array, inputSampleRate, targetSampleRate);
  return floatTo16BitPCM(downsampled);
}
