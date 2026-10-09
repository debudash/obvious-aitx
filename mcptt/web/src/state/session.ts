// Session wiring: owns the ReconnectingSocket + ApiClient pair for one logged-in
// console, feeds every WSS frame into the reducer, re-bootstraps after each
// reconnect (frames missed while offline are recovered from REST), and exposes
// the dispatcher actions. The reducer stays pure; this is the only impure seam.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ApiClient } from '../api/client'
import { ReconnectingSocket, wsUrl } from '../ws/socket'
import {
  consoleReducer,
  initialConsoleState,
  type ConsoleAction,
  type ConsoleState,
} from './consoleStore'
import { parseFrame } from '../protocol/messages'

export interface ConsoleActions {
  endCall(callId: string): Promise<void>
  revokeFloor(callId: string): Promise<void>
  removeParticipant(callId: string, userId: string): Promise<void>
  ackAlert(alertId: string): Promise<void>
  startBroadcast(groupId: string): Promise<void>
  emergencyCall(input: {
    groupId?: string
    callId?: string
    imminentPeril?: boolean
  }): Promise<void>
  deaffiliate(groupId: string, userId?: string): Promise<void>
}

export interface ConsoleSession {
  state: ConsoleState
  /** Latest transient error for the toast; null when clear. */
  error: string | null
  clearError(): void
  /** Re-fetch all REST snapshots (admin panels call this after mutations). */
  refresh(): Promise<void>
  actions: ConsoleActions
}

export function useConsoleSession(token: string | null): ConsoleSession {
  const [state, setState] = useState<ConsoleState>(initialConsoleState)
  const [error, setError] = useState<string | null>(null)

  const dispatch = useCallback((action: ConsoleAction) => {
    setState((prev) => consoleReducer(prev, action))
  }, [])

  // Ref so socket callbacks always see the current client without re-opening
  // the socket on every render.
  const apiRef = useRef<ApiClient | null>(null)

  const bootstrap = useCallback(async () => {
    const api = apiRef.current
    if (!api) return
    // Parallel fetches; a failure here usually means a stale token.
    const [me, users, groups, affiliations, alerts] = await Promise.all([
      api.me(),
      api.listUsers(),
      api.listGroups(),
      api.listAffiliations(),
      api.listAlerts(),
    ])
    dispatch({
      type: 'bootstrap',
      now: Date.now(),
      me,
      users,
      groups,
      affiliations,
      alerts,
    })
  }, [dispatch])

  // Identity change (sign-out, re-login) resets the console — the
  // React-documented adjust-during-render pattern, not an effect.
  const [prevToken, setPrevToken] = useState(token)
  if (prevToken !== token) {
    setPrevToken(token)
    setState(initialConsoleState)
  }

  useEffect(() => {
    if (!token) return
    apiRef.current = new ApiClient(token)

    let disposed = false
    let lastStatus: 'connecting' | 'open' | 'closed' | null = null

    const socket = new ReconnectingSocket(wsUrl(token), {
      onStatus: (status) => {
        if (disposed || status === lastStatus) return
        lastStatus = status
        dispatch({ type: 'socketStatus', status })
        // Every (re)open is a resync point: REST truth heals any frames
        // missed while the transport was down.
        if (status === 'open') {
          bootstrap().catch((err: unknown) => {
            setError(err instanceof Error ? err.message : String(err))
          })
        }
      },
      onMessage: (data) => {
        if (disposed) return
        // parseFrame drops unknown/malformed types (server's own posture
        // mirrored client-side) so one bad frame never breaks the console.
        const frame = parseFrame(data)
        if (frame) dispatch({ type: 'frame', now: Date.now(), frame })
      },
    })
    socket.connect()

    return () => {
      disposed = true
      socket.close()
      apiRef.current = null
      lastStatus = null
    }
  }, [token, dispatch, bootstrap])

  const guard = useCallback(async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  const actions = useMemo<ConsoleActions>(
    () => ({
      endCall: (callId) => guard(() => apiRef.current!.endCall(callId)),
      revokeFloor: (callId) => guard(() => apiRef.current!.revokeFloor(callId)),
      removeParticipant: (callId, userId) =>
        guard(() => apiRef.current!.removeParticipant(callId, userId)),
      ackAlert: (alertId) => guard(() => apiRef.current!.ackAlert(alertId)),
      startBroadcast: (groupId) =>
        guard(async () => {
          const res = await apiRef.current!.startBroadcast(groupId)
          dispatch({ type: 'callSnapshot', now: Date.now(), call: res.call })
        }),
      emergencyCall: (input) =>
        guard(async () => {
          const res = await apiRef.current!.emergencyCall(input)
          dispatch({ type: 'callSnapshot', now: Date.now(), call: res.call })
        }),
      deaffiliate: (groupId, userId) =>
        guard(() => apiRef.current!.deaffiliate(groupId, userId)),
    }),
    [dispatch, guard],
  )

  return { state, error, clearError: () => setError(null), refresh: bootstrap, actions }
}
