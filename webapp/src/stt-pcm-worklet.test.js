import { PCM16StreamResampler } from './stt-pcm-worklet';

function resampleOneSecond(inputSampleRate) {
  const resampler = new PCM16StreamResampler(inputSampleRate);
  const output = [];
  for (let offset = 0; offset < inputSampleRate; offset += 128) {
    const length = Math.min(128, inputSampleRate - offset);
    const input = new Float32Array(length);
    for (let index = 0; index < length; index += 1) {
      input[index] = Math.sin(((offset + index) / inputSampleRate) * Math.PI * 2 * 440);
    }
    output.push(...resampler.push(input));
  }
  output.push(...resampler.flush());
  return output.reduce((total, buffer) => total + (buffer.byteLength / 2), 0);
}

describe('PCM16StreamResampler', () => {
  it.each([44100, 48000])('produces exactly 16 kHz for one second of %i Hz input', (sampleRate) => {
    expect(resampleOneSecond(sampleRate)).toBe(16000);
  });

  it('flushes the final frame without padding it to 100 ms', () => {
    const resampler = new PCM16StreamResampler(48000);
    const fullFrames = resampler.push(new Float32Array(2400));
    const tail = resampler.flush();

    expect(fullFrames).toHaveLength(0);
    expect(tail).toHaveLength(1);
    expect(tail[0].byteLength).toBeGreaterThan(0);
    expect(tail[0].byteLength).toBeLessThan(3200);
  });
});
