// Reconnecting WSS client. The server fans out broadcast-only frames and
// detects dead peers with ping/pong; the client's job is to hold one socket,
// parse frames, and reconnect with backoff when the transport drops. The
// socket factory is injectable so tests can drive the state layer without a
// real server.

export interface WebSocketLike {
  send(data: string): void
  close(code?: number): void
  set onopen(handler: () => void)
  set onclose(handler: () => void)
  set onerror(handler: () => void)
  set onmessage(handler: (event: { data: unknown }) => void)
}

export type SocketFactory = (url: string) => WebSocketLike

export type SocketStatus = 'connecting' | 'open' | 'closed'

export interface SocketHandlers {
  onMessage(data: string): void
  onStatus(status: SocketStatus): void
}

const BASE_BACKOFF_MS = 250
const MAX_BACKOFF_MS = 10_000

/** Exponential backoff with jitter, capped. Pure — directly unit-testable. */
export function backoffDelay(attempt: number, random = Math.random()): number {
  const exp = Math.min(attempt, 30)
  const base = Math.min(BASE_BACKOFF_MS * 2 ** exp, MAX_BACKOFF_MS)
  const jitter = random * Math.min(BASE_BACKOFF_MS, base / 2)
  return Math.min(base + jitter, MAX_BACKOFF_MS + BASE_BACKOFF_MS)
}

export class ReconnectingSocket {
  private handlers: SocketHandlers
  private factory: SocketFactory
  private url: string
  private socket: WebSocketLike | null = null
  private attempt = 0
  private timer: ReturnType<typeof setTimeout> | null = null
  private closedByUser = false

  constructor(url: string, handlers: SocketHandlers, factory?: SocketFactory) {
    this.url = url
    this.handlers = handlers
    this.factory = factory ?? defaultFactory
  }

  connect(): void {
    if (this.socket || this.closedByUser) return
    this.handlers.onStatus('connecting')
    this.open()
  }

  /** Permanently close — no reconnect after an explicit close. */
  close(): void {
    this.closedByUser = true
    if (this.timer) clearTimeout(this.timer)
    this.timer = null
    this.socket?.close()
    this.socket = null
  }

  private open(): void {
    let sock: WebSocketLike
    try {
      sock = this.factory(this.url)
    } catch {
      this.scheduleReconnect()
      return
    }
    this.socket = sock
    sock.onopen = () => {
      this.attempt = 0
      this.handlers.onStatus('open')
    }
    sock.onmessage = (event) => {
      if (typeof event.data === 'string') this.handlers.onMessage(event.data)
    }
    sock.onclose = () => {
      if (this.socket === sock) this.socket = null
      this.scheduleReconnect()
    }
    sock.onerror = () => {
      // onclose follows in browsers; nothing to do here but report state.
      this.handlers.onStatus('connecting')
    }
  }

  private scheduleReconnect(): void {
    if (this.closedByUser || this.timer) return
    this.handlers.onStatus('closed')
    const delay = backoffDelay(this.attempt)
    this.attempt += 1
    this.timer = setTimeout(() => {
      this.timer = null
      if (!this.closedByUser) this.open()
    }, delay)
  }
}

function defaultFactory(url: string): WebSocketLike {
  // Browsers deliver the real WebSocket; the like-interface keeps tests
  // hermetic.
  return new WebSocket(url) as unknown as WebSocketLike
}

/** Build the /ws URL for the current origin (same-origin via the dev/preview proxy). */
export function wsUrl(token: string, origin = window.location.origin): string {
  const wsOrigin = origin.replace(/^http/, 'ws')
  return `${wsOrigin}/ws?token=${encodeURIComponent(token)}`
}
