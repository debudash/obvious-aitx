// REST client for the MCPTT server. Every method maps one router.go route.
// Requests are same-origin (the dev/preview proxy forwards to the server —
// the server sets no CORS headers).
import type { Affiliation, Alert, Group, LoginResponse, SessionInfo, User } from '../protocol/messages'

export class ApiError extends Error {
  readonly status: number
  /** Field problems from 400 validation responses (`{error, problems[]}`). */
  readonly problems?: string[]

  constructor(status: number, message: string, problems?: string[]) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.problems = problems
  }
}

async function decode(res: Response): Promise<unknown> {
  try {
    return await res.json()
  } catch {
    return {}
  }
}

export class ApiClient {
  private token: string

  constructor(token: string) {
    this.token = token
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    let res: Response
    try {
      res = await fetch(path, {
        method,
        headers: {
          Authorization: `Bearer ${this.token}`,
          ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
      })
    } catch (err) {
      // Network-level failure (server down, proxy down) — keep the cause.
      throw new ApiError(0, `Network error: ${err instanceof Error ? err.message : String(err)}`)
    }
    const data = await decode(res)
    if (!res.ok) {
      const envelope = data as { error?: string; problems?: string[] }
      throw new ApiError(
        res.status,
        envelope.error ?? `${res.status} ${res.statusText}`,
        envelope.problems,
      )
    }
    return data as T
  }

  static async login(username: string, password: string): Promise<LoginResponse> {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
    const data = (await decode(res)) as Partial<LoginResponse> & { error?: string }
    if (!res.ok || !data.token || !data.user) {
      throw new ApiError(res.status, data.error ?? 'invalid credentials')
    }
    return data as LoginResponse
  }

  me(): Promise<User> {
    return this.request<User>('GET', '/api/users/me')
  }

  listUsers(): Promise<User[]> {
    return this.request<User[]>('GET', '/api/users')
  }

  updateUser(
    id: string,
    patch: { displayName?: string; role?: string; priority?: number; functionalAlias?: string },
  ): Promise<User> {
    return this.request<User>('PATCH', `/api/users/${id}`, patch)
  }

  register(input: {
    username: string
    password: string
    displayName: string
    role: string
    priority?: number
    functionalAlias?: string
  }): Promise<{ user: User; token: string }> {
    return this.request<{ user: User; token: string }>('POST', '/api/auth/register', input)
  }

  listGroups(): Promise<Group[]> {
    return this.request<Group[]>('GET', '/api/groups')
  }

  createGroup(name: string, description: string): Promise<Group> {
    return this.request<Group>('POST', '/api/groups', { name, description })
  }

  updateGroup(id: string, name: string, description: string): Promise<Group> {
    return this.request<Group>('PATCH', `/api/groups/${id}`, { name, description })
  }

  deleteGroup(id: string): Promise<void> {
    return this.request<void>('DELETE', `/api/groups/${id}`)
  }

  addMember(groupId: string, userId: string): Promise<void> {
    return this.request<void>('POST', `/api/groups/${groupId}/members`, { userId })
  }

  removeMember(groupId: string, userId: string): Promise<void> {
    return this.request<void>('DELETE', `/api/groups/${groupId}/members/${userId}`)
  }

  listAffiliations(userId?: string): Promise<Affiliation[]> {
    const q = userId ? `?userId=${encodeURIComponent(userId)}` : ''
    return this.request<Affiliation[]>('GET', `/api/affiliations${q}`)
  }

  affiliate(groupId: string, userId?: string): Promise<Affiliation> {
    return this.request<Affiliation>('POST', `/api/groups/${groupId}/affiliations`, {
      userId: userId ?? '',
    })
  }

  deaffiliate(groupId: string, userId?: string): Promise<void> {
    return this.request<void>('DELETE', `/api/groups/${groupId}/affiliations`, {
      userId: userId ?? '',
    })
  }

  listAlerts(status?: string): Promise<Alert[]> {
    const q = status ? `?status=${encodeURIComponent(status)}` : ''
    return this.request<Alert[]>('GET', `/api/emergency/alerts${q}`)
  }

  ackAlert(alertId: string): Promise<Alert> {
    return this.request<Alert>('POST', `/api/emergency/alerts/${alertId}/ack`)
  }

  /** Emergency group call (any user): starts + escalates + raises the alert. */
  emergencyCall(input: {
    groupId?: string
    callId?: string
    imminentPeril?: boolean
    lat?: number
    lon?: number
    note?: string
  }): Promise<{ call: SessionInfo; decision: FloorDecisionJSON; alert: Alert }> {
    return this.request('POST', '/api/calls/emergency', input)
  }

  startBroadcast(groupId: string): Promise<{ call: SessionInfo }> {
    return this.request<{ call: SessionInfo }>('POST', '/api/dispatch/broadcast', { groupId })
  }

  endCall(callId: string): Promise<void> {
    return this.request<void>('POST', `/api/dispatch/calls/${callId}/end`)
  }

  revokeFloor(callId: string): Promise<{ revoked: string }> {
    return this.request<{ revoked: string }>('POST', `/api/dispatch/calls/${callId}/revoke`)
  }

  removeParticipant(callId: string, userId: string): Promise<{ removed: string }> {
    return this.request<{ removed: string }>(
      'DELETE',
      `/api/dispatch/calls/${callId}/participants/${userId}`,
    )
  }
}

// Local type alias to avoid importing the decision shape twice in signatures.
type FloorDecisionJSON = {
  userId: string
  outcome: 'granted' | 'queued' | 'denied'
  priority: number
  reason?: string
}
