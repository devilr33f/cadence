// a websocket the tests drive: open it, hand it frames, close it as the server would
export class FakeSocket {
  static instances: FakeSocket[] = []

  readyState = 0
  readonly sent: string[] = []
  onopen: ((event: unknown) => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: ((event: { code: number, reason: string }) => void) | null = null
  onerror: ((event: unknown) => void) | null = null
  closedWith?: number

  constructor(readonly url: string) {
    FakeSocket.instances.push(this)
  }

  static reset() {
    FakeSocket.instances = []
  }

  static get last(): FakeSocket {
    const socket = FakeSocket.instances.at(-1)
    if (!socket)
      throw new Error('no socket was opened')
    return socket
  }

  send(data: string) {
    if (this.readyState !== 1)
      throw new Error('send on a socket that is not open')
    this.sent.push(data)
  }

  close(code = 1000) {
    this.closedWith = code
    this.readyState = 3
  }

  open() {
    this.readyState = 1
    this.onopen?.({})
  }

  receive(frame: unknown) {
    this.onmessage?.({
      data: JSON.stringify(frame),
    })
  }

  serverClose(code: number, reason = '') {
    this.readyState = 3
    this.onclose?.({ code, reason })
  }

  frames(): unknown[] {
    return this.sent.map(s => JSON.parse(s) as unknown)
  }
}
