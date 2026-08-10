class CatsCoPCM16CaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.ratio = sampleRate / 16000;
    this.carry = new Float32Array(0);
    this.position = 0;
    this.output = new Int16Array(1600);
    this.outputLength = 0;
    this.meterSumSquares = 0;
    this.meterSampleCount = 0;
    this.meterIntervalFrames = Math.max(1, Math.round(sampleRate / 30));
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

    const samples = new Float32Array(this.carry.length + input.length);
    samples.set(this.carry);
    samples.set(input, this.carry.length);

    while (this.position + 1 < samples.length) {
      const left = Math.floor(this.position);
      const fraction = this.position - left;
      const sample = samples[left] + (samples[left + 1] - samples[left]) * fraction;
      const clamped = Math.max(-1, Math.min(1, sample));
      this.output[this.outputLength] = clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff;
      this.outputLength += 1;
      this.position += this.ratio;

      if (this.outputLength === this.output.length) {
        const buffer = this.output.buffer;
        this.port.postMessage(buffer, [buffer]);
        this.output = new Int16Array(1600);
        this.outputLength = 0;
      }
    }

    const consumed = Math.floor(this.position);
    this.carry = samples.slice(consumed);
    this.position -= consumed;
    return true;
  }
}

registerProcessor('catsco-pcm16-capture', CatsCoPCM16CaptureProcessor);
