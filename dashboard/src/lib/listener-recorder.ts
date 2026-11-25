// the browser's microphone for a listener calibration: float pcm at the audio context's rate, in this page's memory
// alone, never sent anywhere and dropped once measured (rule 8). the phone's own processing is off, so the chirps
// arrive as the room carried them

export interface Recorder {
  rate: number
  // the recording's frame now (the audio context's clock); any constant input latency cancels, as only the chirps'
  // arrivals against each other count
  frameNow: () => number
  // stops the microphone and answers what it heard; a second call answers an empty recording
  stop: () => Float32Array
}

// a secure context with a microphone and audio worklets: the dashboard over https, on a phone or a laptop
export function listenerSupported(): boolean {
  return typeof window !== 'undefined'
    && window.isSecureContext
    && typeof navigator.mediaDevices?.getUserMedia === 'function'
    && typeof AudioContext !== 'undefined'
    && typeof AudioWorkletNode !== 'undefined'
}

const PROCESSOR = 'cadence-listener'
const CHUNK = 4096

// posts the input's first channel in chunks, and the frame it started at; a quantum with no input is silence, so the
// frames stay in step with the context's clock
const WORKLET = `
class Listener extends AudioWorkletProcessor {
  constructor() {
    super()
    this.chunk = new Float32Array(${CHUNK})
    this.filled = 0
    this.started = false
  }
  process(inputs) {
    if (!this.started) {
      this.started = true
      this.port.postMessage({ start: currentFrame })
    }
    const channel = inputs[0] && inputs[0][0]
    const frames = channel ? channel.length : 128
    for (let i = 0; i < frames; i++) {
      this.chunk[this.filled++] = channel ? channel[i] : 0
      if (this.filled === ${CHUNK}) {
        this.port.postMessage({ chunk: this.chunk }, [this.chunk.buffer])
        this.chunk = new Float32Array(${CHUNK})
        this.filled = 0
      }
    }
    return true
  }
}
registerProcessor('${PROCESSOR}', Listener)
`

// starts recording: called from the owner's press, as the audio context must start in a user gesture. answers once
// the first frames came in
export async function startRecorder(): Promise<Recorder> {
  // made before any await, while the press still counts as a gesture
  const context = new AudioContext()
  void context.resume()
  let stream: MediaStream | undefined
  let moduleUrl: string | undefined
  const chunks: Float32Array[] = []
  let startFrame = 0
  let stopped = false
  const release = () => {
    stream?.getTracks().forEach(track => track.stop())
    void context.close()
    if (moduleUrl)
      URL.revokeObjectURL(moduleUrl)
  }
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: false,
        noiseSuppression: false,
        autoGainControl: false,
        channelCount: 1,
      },
    })
    moduleUrl = URL.createObjectURL(new Blob([WORKLET], { type: 'text/javascript' }))
    await context.audioWorklet.addModule(moduleUrl)
    const source = context.createMediaStreamSource(stream)
    const node = new AudioWorkletNode(context, PROCESSOR, {
      numberOfInputs: 1,
      numberOfOutputs: 1,
      outputChannelCount: [1],
    })
    const started = new Promise<void>((resolve) => {
      node.port.onmessage = (event: MessageEvent<{ start?: number, chunk?: Float32Array }>) => {
        if (stopped)
          return
        if (event.data.start !== undefined) {
          startFrame = event.data.start
          resolve()
        }
        if (event.data.chunk)
          chunks.push(event.data.chunk)
      }
    })
    source.connect(node)
    // the node's output is silence: connected only so the context pulls it
    node.connect(context.destination)
    await context.resume()
    await started
  }
  catch (error) {
    release()
    throw error
  }
  return {
    rate: context.sampleRate,
    frameNow: () => context.currentTime * context.sampleRate - startFrame,
    stop: () => {
      if (stopped)
        return new Float32Array(0)
      stopped = true
      release()
      const total = chunks.reduce((n, c) => n + c.length, 0)
      const out = new Float32Array(total)
      let at = 0
      for (const c of chunks) {
        out.set(c, at)
        at += c.length
      }
      chunks.length = 0
      return out
    },
  }
}
