export class PCM16StreamResampler {
  constructor(inputSampleRate, outputSampleRate = 16000, frameSamples = 1600) {
    this.ratio = inputSampleRate / outputSampleRate;
    this.carry = new Float32Array(0);
    this.position = 0;
    this.output = new Int16Array(frameSamples);
    this.outputLength = 0;
  }

  push(input) {
    if (!input?.length) return [];
    const frames = [];
    const samples = new Float32Array(this.carry.length + input.length);
    samples.set(this.carry);
    samples.set(input, this.carry.length);

    while (this.position + 1 < samples.length) {
      const left = Math.floor(this.position);
      const fraction = this.position - left;
      const sample = samples[left] + ((samples[left + 1] - samples[left]) * fraction);
      const clamped = Math.max(-1, Math.min(1, sample));
      this.output[this.outputLength] = clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff;
      this.outputLength += 1;
      this.position += this.ratio;

      if (this.outputLength === this.output.length) frames.push(this.takeOutput());
    }

    // Always retain the final source sample so interpolation phase survives
    // across AudioWorklet render quanta.
    const consumed = Math.min(Math.floor(this.position), samples.length - 1);
    this.carry = samples.slice(consumed);
    this.position -= consumed;
    return frames;
  }

  flush() {
    if (this.outputLength === 0) return [];
    return [this.takeOutput(true)];
  }

  takeOutput(trim = false) {
    const output = trim ? this.output.slice(0, this.outputLength) : this.output;
    const buffer = output.buffer;
    this.output = new Int16Array(this.output.length);
    this.outputLength = 0;
    return buffer;
  }
}

const WorkletBase = globalThis.AudioWorkletProcessor || class {};

class CatsCoPCM16CaptureProcessor extends WorkletBase {
  constructor() {
    super();
    this.resampler = new PCM16StreamResampler(sampleRate);
    this.meterSumSquares = 0;
    this.meterSampleCount = 0;
    this.meterIntervalFrames = Math.max(1, Math.round(sampleRate / 30));
    this.port.onmessage = (event) => {
      if (event.data?.type !== 'flush') return;
      for (const buffer of this.resampler.flush()) this.port.postMessage(buffer, [buffer]);
      this.port.postMessage({ type: 'flushed' });
    };
  }

  process(inputs) {
    const input = inputs[0]?.[0];
    if (!input?.length) return true;

    for (let index = 0; index < input.length; index += 1) {
      const sample = Math.max(-1, Math.min(1, input[index]));
      this.meterSumSquares += sample * sample;
    }
    this.meterSampleCount += input.length;
    if (this.meterSampleCount >= this.meterIntervalFrames) {
      this.port.postMessage({
        type: 'level',
        rms: Math.sqrt(this.meterSumSquares / this.meterSampleCount),
      });
      this.meterSumSquares = 0;
      this.meterSampleCount = 0;
    }

    for (const buffer of this.resampler.push(input)) this.port.postMessage(buffer, [buffer]);
    return true;
  }
}

if (typeof globalThis.registerProcessor === 'function') {
  globalThis.registerProcessor('catsco-pcm16-capture', CatsCoPCM16CaptureProcessor);
}
