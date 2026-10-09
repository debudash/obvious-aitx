// Reconnecting-socket tests with an injected mock factory and fake timers:
// status transitions, reconnect backoff, and user-close semantics.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ReconnectingSocket, backoffDelay, type WebSocketLike } from './socket'

class FakeSocket implements WebSocketLike {
  sent: string[] = []
  closed = 0
  onopen: () => void = () => {}
  onclose: () => void = () => {}
  onerror: () => void = () => {}
  onmessage: (event: { data: unknown }) => void = () => {}
  send(data: string): void {
    this.sent.push(data)
  }
  close(): void {
    this.closed += 1
  }
}

describe('backoffDelay', () => {
  it('grows exponentially from 250ms with bounded jitter', () => {
    expect(backoffDelay(0, 0)).toBe(250)
    expect(backoffDelay(1, 0)).toBe(500)
    expect(backoffDelay(2, 0)).toBe(1000)
  })

  it('adds jitter that never halves the slot', () => {
    for (let attempt = 0; attempt < 8; attempt++) {
      for (const r of [0, 0.5, 1]) {
        const d = backoffDelay(attempt, r)
        expect(d).toBeGreaterThan(0)
        expect(d).toBeLessThanOrEqual(10_250)
      }
    }
  })

  it('caps at 10s plus max jitter', () => {
    expect(backoffDelay(30, 1)).toBe(10_250)
    expect(backoffDelay(1000, 0)).toBe(10_000)
  })
})

describe('ReconnectingSocket', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  function harness() {
    const sockets: FakeSocket[] = []
    const statuses: string[] = []
    const messages: string[] = []
    const socket = new ReconnectingSocket(
      'wss://example/ws?token=t',
      {
        onStatus: (s) => statuses.push(s),
        onMessage: (d) => messages.push(d),
      },
      () => {
        const s = new FakeSocket()
        sockets.push(s)
        return s
      },
    )
    return { sockets, statuses, messages, socket }
  }

  it('reports connecting then open, and delivers string messages', () => {
    const h = harness()
    h.socket.connect()
    expect(h.statuses).toEqual(['connecting'])
    h.sockets[0]!.onopen()
    expect(h.statuses).toEqual(['connecting', 'open'])
    h.sockets[0]!.onmessage({ data: '{"type":"CallStarted"}' })
    expect(h.messages).toEqual(['{"type":"CallStarted"}'])
  })

  it('reconnects with backoff after an abnormal close', () => {
    const h = harness()
    h.socket.connect()
    expect(h.sockets).toHaveLength(1)
    h.sockets[0]!.onclose()
    expect(h.statuses.at(-1)).toBe('closed')
    vi.advanceTimersByTime(500)
    expect(h.sockets).toHaveLength(2)
  })

  it('does not reconnect after an explicit close', () => {
    const h = harness()
    h.socket.connect()
    h.socket.close()
    expect(h.sockets[0]!.closed).toBe(1)
    vi.advanceTimersByTime(60_000)
    expect(h.sockets).toHaveLength(1)
  })

  it('ignores non-string message payloads', () => {
    const h = harness()
    h.socket.connect()
    h.sockets[0]!.onmessage({ data: new Uint8Array([1, 2, 3]) })
    h.sockets[0]!.onmessage({ data: 42 })
    expect(h.messages).toEqual([])
  })

  it('keeps the same socket across open without duplicate sockets', () => {
    const h = harness()
    h.socket.connect()
    h.socket.connect()
    expect(h.sockets).toHaveLength(1)
  })
})
