// radix-2 transforms and analytic correlation behind the coded detector, following sync/calibration/fft.cpp and detect.cpp

export interface Correlation {
  re: Float64Array
  im: Float64Array
}

function nextPow2(n: number): number {
  let out = 1
  while (out < n)
    out *= 2
  return out
}

// in-place, with the inverse left unnormalized
export function fft(re: Float64Array, im: Float64Array, inverse: boolean): void {
  const n = re.length
  if (n < 2 || (n & (n - 1)) !== 0)
    return
  for (let i = 1, j = 0; i < n; i++) {
    let bit = n >> 1
    for (; (j & bit) !== 0; bit >>= 1)
      j ^= bit
    j ^= bit
    if (i < j) {
      [re[i], re[j]] = [re[j]!, re[i]!]
      ;[im[i], im[j]] = [im[j]!, im[i]!]
    }
  }
  for (let length = 2; length <= n; length *= 2) {
    const half = length / 2
    const angle = (inverse
      ? 2
      : -2) * Math.PI / length
    for (let k = 0; k < half; k++) {
      // fresh twiddles keep recurrence rounding out of long transforms
      const wr = Math.cos(angle * k)
      const wi = Math.sin(angle * k)
      for (let first = 0; first < n; first += length) {
        const a = first + k
        const b = a + half
        const vr = re[b]! * wr - im[b]! * wi
        const vi = re[b]! * wi + im[b]! * wr
        re[b] = re[a]! - vr
        im[b] = im[a]! - vi
        re[a] = re[a]! + vr
        im[a] = im[a]! + vi
      }
    }
  }
}

// one reference, with its transform cached by fft size for a run's many windows
export class Correlator {
  readonly refSize: number
  private readonly ref: Float32Array
  private readonly spectra = new Map<number, Correlation>()

  constructor(ref: Float32Array) {
    this.ref = ref
    this.refSize = ref.length
  }

  correlate(signal: Float32Array): Correlation {
    if (this.ref.length === 0 || this.ref.length > signal.length) {
      return {
        re: new Float64Array(0),
        im: new Float64Array(0),
      }
    }
    const n = nextPow2(signal.length + this.ref.length)
    let r = this.spectra.get(n)
    if (!r) {
      r = {
        re: new Float64Array(n),
        im: new Float64Array(n),
      }
      r.re.set(this.ref)
      fft(r.re, r.im, false)
      this.spectra.set(n, r)
    }
    const sr = new Float64Array(n)
    const si = new Float64Array(n)
    sr.set(signal)
    fft(sr, si, false)
    for (let k = 0; k < n; k++) {
      const xr = sr[k]! * r.re[k]! + si[k]! * r.im[k]!
      const xi = si[k]! * r.re[k]! - sr[k]! * r.im[k]!
      const scale = k === 0 || k === n / 2
        ? 1
        : k < n / 2
          ? 2
          : 0
      sr[k] = xr * scale
      si[k] = xi * scale
    }
    fft(sr, si, true)
    const lags = signal.length - this.ref.length + 1
    const re = new Float64Array(lags)
    const im = new Float64Array(lags)
    for (let k = 0; k < lags; k++) {
      re[k] = sr[k]! / n
      im[k] = si[k]! / n
    }
    return { re, im }
  }
}
