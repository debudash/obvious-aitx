// The console state layer: one pure reducer consuming (a) REST bootstrap
// snapshots and (b) every WSS protocol frame the server broadcasts. This is
// the file unit tests drive with a mock socket — the reducer itself never
// touches the network, so the full floor lifecycle (grant, queue, deny,
// pre-empt, release, auto-grant) is provable without a server.
import type {
  Affiliation,
  Alert,
  CallKind,
  Group,
  ServerFrame,
  SessionInfo,
  User,
} from '../protocol/messages'

export type PresenceState = 'online' | 'offline'

export type ConnectionStatus = 'connecting' | 'open' | 'closed'

/** Client-visible state of one live call, assembled from frames + snapshots. */
export interface CallState {
  callId: string
  kind: CallKind
  groupId?: string
  emergency: boolean
  imminentPeril: boolean
  participants: string[]
  speaker?: string
  /** epoch millis — frame arrival time or the snapshot's speakerSince */
  speakerSince?: number
  queue: string[]
  /** Set when FloorPreempted fired on this call; describes the cut-off. */
  preemption?: { by: string; emergency: boolean; at: number }
}

/** One line of the Activity view: everything this console observed live. */
export interface EventLogEntry {
  id: number
  at: number
  kind: string
  text: string
}

/** Why the last floor request from a user failed (denied toast source). */
export interface FloorDenial {
  callId: string
  reason: string
  queuePosition?: number
  at: number
}

export interface ConsoleState {
  connection: ConnectionStatus
  me: User | null
  users: Record<string, User>
  groups: Record<string, Group>
  affiliations: Affiliation[]
  presence: Record<string, PresenceState>
  calls: Record<string, CallState>
  /** Active (unacknowledged) alerts, newest first. */
  activeAlerts: Alert[]
  /** Acknowledged alerts, newest first (the rail's archive). */
  archivedAlerts: Alert[]
  denials: Record<string, FloorDenial>
  eventLog: EventLogEntry[]
}

export const EVENT_LOG_CAP = 500

export const initialConsoleState: ConsoleState = {
  connection: 'connecting',
  me: null,
  users: {},
  groups: {},
  affiliations: [],
  presence: {},
  calls: {},
  activeAlerts: [],
  archivedAlerts: [],
  denials: {},
  eventLog: [],
}

export type ConsoleAction =
  | {
      type: 'bootstrap'
      now: number
      me: User
      users: User[]
      groups: Group[]
      affiliations: Affiliation[]
      alerts: Alert[]
    }
  | { type: 'socketStatus'; status: ConnectionStatus }
  | { type: 'frame'; now: number; frame: ServerFrame }
  /** REST-returned authoritative snapshot of one call (on create/join). */
  | { type: 'callSnapshot'; now: number; call: SessionInfo }
  | { type: 'reset' }

let logIdCounter = 0

function log(state: ConsoleState, at: number, kind: string, text: string): EventLogEntry[] {
  const entry: EventLogEntry = { id: ++logIdCounter, at, kind, text }
  const next = [entry, ...state.eventLog]
  return next.length > EVENT_LOG_CAP ? next.slice(0, EVENT_LOG_CAP) : next
}

function userName(state: ConsoleState, userId: string): string {
  return state.users[userId]?.displayName ?? userId
}

function groupName(state: ConsoleState, groupId: string | undefined): string {
  if (!groupId) return ''
  return state.groups[groupId]?.name ?? groupId
}

function updateCall(
  state: ConsoleState,
  callId: string,
  patch: (c: CallState) => CallState,
): Record<string, CallState> {
  const existing = state.calls[callId]
  if (!existing) return state.calls
  return { ...state.calls, [callId]: patch(existing) }
}

function withoutDenial(
  denials: Record<string, FloorDenial>,
  userId: string,
  callId: string,
): Record<string, FloorDenial> {
  const d = denials[userId]
  if (!d || d.callId !== callId) return denials
  const next = { ...denials }
  delete next[userId]
  return next
}

function reduceFrame(state: ConsoleState, now: number, frame: ServerFrame): ConsoleState {
  switch (frame.type) {
    case 'CallStart':
      return {
        ...state,
        calls: {
          ...state.calls,
          [frame.callId]: {
            callId: frame.callId,
            kind: frame.kind,
            groupId: frame.groupId,
            emergency: false,
            imminentPeril: false,
            participants: [frame.initiatorId],
            queue: [],
          },
        },
        eventLog: log(
          state,
          now,
          'call.started',
          `${kindLabel(frame.kind)} call started by ${userName(state, frame.initiatorId)}` +
            (frame.groupId ? ` on ${groupName(state, frame.groupId)}` : ''),
        ),
      }

    case 'CallJoined': {
      const calls = updateCall(state, frame.callId, (c) => ({
        ...c,
        participants: c.participants.includes(frame.userId)
          ? c.participants
          : [...c.participants, frame.userId],
      }))
      return {
        ...state,
        calls,
        eventLog: log(state, now, 'call.joined', `${userName(state, frame.userId)} joined the call`),
      }
    }

    case 'ParticipantRemoved':
      return {
        ...state,
        calls: updateCall(state, frame.callId, (c) => ({
          ...c,
          participants: c.participants.filter((p) => p !== frame.userId),
          speaker: c.speaker === frame.userId ? undefined : c.speaker,
          speakerSince: c.speaker === frame.userId ? undefined : c.speakerSince,
          queue: c.queue.filter((q) => q !== frame.userId),
        })),
        eventLog: log(
          state,
          now,
          'call.removed',
          `${userName(state, frame.by)} removed ${userName(state, frame.userId)} from the call`,
        ),
      }

    case 'CallEnded': {
      const calls = { ...state.calls }
      delete calls[frame.callId]
      return {
        ...state,
        calls,
        eventLog: log(state, now, 'call.ended', `Call ended by ${userName(state, frame.by)}`),
      }
    }

    case 'FloorGranted': {
      const calls = updateCall(state, frame.callId, (c) => ({
        ...c,
        speaker: frame.userId,
        speakerSince: now,
        queue: frame.queue ?? [],
      }))
      return {
        ...state,
        calls,
        denials: withoutDenial(state.denials, frame.userId, frame.callId),
        eventLog: log(
          state,
          now,
          'floor.granted',
          `${userName(state, frame.userId)} holds the floor` +
            (frame.queue && frame.queue.length > 0
              ? ` — queue: ${frame.queue.map((u) => userName(state, u)).join(', ')}`
              : ''),
        ),
      }
    }

    case 'FloorDenied': {
      const queued = frame.queuePosition > 0
      return {
        ...state,
        denials: {
          ...state.denials,
          [frame.userId]: {
            callId: frame.callId,
            reason: frame.reason,
            queuePosition: frame.queuePosition || undefined,
            at: now,
          },
        },
        eventLog: log(
          state,
          now,
          queued ? 'floor.queued' : 'floor.denied',
          queued
            ? `${userName(state, frame.userId)} queued at #${frame.queuePosition}`
            : `${userName(state, frame.userId)} denied — ${frame.reason}`,
        ),
      }
    }

    case 'FloorReleased':
      return {
        ...state,
        calls: updateCall(state, frame.callId, (c) => ({
          ...c,
          speaker: c.speaker === frame.userId ? undefined : c.speaker,
          speakerSince: c.speaker === frame.userId ? undefined : c.speakerSince,
        })),
        eventLog: log(
          state,
          now,
          'floor.released',
          `${userName(state, frame.userId)} released the floor`,
        ),
      }

    case 'FloorPreempted':
      return {
        ...state,
        calls: updateCall(state, frame.callId, (c) => ({
          ...c,
          speaker: undefined,
          speakerSince: undefined,
          preemption: { by: frame.by, emergency: frame.emergency, at: now },
        })),
        eventLog: log(
          state,
          now,
          'floor.preempted',
          `${userName(state, frame.by)} pre-empted the floor${frame.emergency ? ' (EMERGENCY)' : ''}`,
        ),
      }

    case 'EmergencyAlert': {
      const alert: Alert = {
        id: frame.alertId,
        userId: frame.userId,
        callId: frame.callId,
        kind: frame.imminentPeril ? 'imminent_peril' : 'emergency',
        lat: frame.lat,
        lon: frame.lon,
        note: frame.note,
        status: 'active',
        createdAt: new Date(now).toISOString(),
      }
      const alreadyActive = state.activeAlerts.some((a) => a.id === alert.id)
      return {
        ...state,
        activeAlerts: alreadyActive ? state.activeAlerts : [alert, ...state.activeAlerts],
        calls: frame.callId
          ? updateCall(state, frame.callId, (c) => ({
              ...c,
              emergency: true,
              imminentPeril: c.imminentPeril || frame.imminentPeril === true,
            }))
          : state.calls,
        eventLog: log(
          state,
          now,
          frame.imminentPeril ? 'emergency.imminent_peril' : 'emergency.alert',
          `EMERGENCY from ${userName(state, frame.userId)}` +
            (frame.callId ? ' — linked to the live call' : ' — voiceless alert') +
            (frame.lat !== undefined && frame.lon !== undefined
              ? ` — located ${frame.lat.toFixed(4)}, ${frame.lon.toFixed(4)}`
              : ' — no location'),
        ),
      }
    }

    case 'EmergencyAlertAck': {
      const active = state.activeAlerts.find((a) => a.id === frame.alertId)
      const acked: Alert | undefined = active
        ? { ...active, status: 'acknowledged', acknowledgedBy: frame.acknowledgedBy }
        : undefined
      return {
        ...state,
        activeAlerts: state.activeAlerts.filter((a) => a.id !== frame.alertId),
        archivedAlerts: acked ? [acked, ...state.archivedAlerts] : state.archivedAlerts,
        eventLog: log(
          state,
          now,
          'emergency.acknowledged',
          `Alert acknowledged by ${userName(state, frame.acknowledgedBy)}`,
        ),
      }
    }

    case 'PresenceUpdate':
      return {
        ...state,
        presence: { ...state.presence, [frame.userId]: frame.state },
      }

    case 'AffiliationChanged': {
      const idx = state.affiliations.findIndex(
        (a) => a.userId === frame.userId && a.groupId === frame.groupId,
      )
      const row: Affiliation = {
        userId: frame.userId,
        groupId: frame.groupId,
        state: frame.state,
        changedAt: new Date(frame.at).toISOString(),
      }
      const affiliations =
        idx === -1
          ? [...state.affiliations, row]
          : state.affiliations.map((a, i) => (i === idx ? row : a))
      return {
        ...state,
        affiliations,
        eventLog: log(
          state,
          now,
          'affiliation.changed',
          `${userName(state, frame.userId)} ${frame.state} with ${groupName(state, frame.groupId)}`,
        ),
      }
    }

    case 'MediaAnswer':
      // Browser media is a documented follow-up (no room-token route on this
      // server build); an error answer still surfaces in the log.
      if (frame.err) {
        return {
          ...state,
          eventLog: log(state, now, 'media.rejected', `Media offer rejected: ${frame.err}`),
        }
      }
      return state

    default:
      return state
  }
}

export function kindLabel(kind: CallKind): string {
  switch (kind) {
    case 'group':
      return 'Group'
    case 'private':
      return 'Private'
    case 'broadcast':
      return 'Broadcast'
  }
}

function reduceSnapshot(state: ConsoleState, call: SessionInfo): ConsoleState {
  const existing = state.calls[call.callId]
  const next: CallState = {
    callId: call.callId,
    kind: call.kind,
    groupId: call.groupId,
    emergency: call.emergency,
    imminentPeril: call.imminentPeril,
    participants: call.participants,
    speaker: call.speaker,
    speakerSince: call.speakerSince ? Date.parse(call.speakerSince) : existing?.speakerSince,
    queue: call.queue ?? [],
    // Keep an existing pre-emption notice; snapshots never carry one.
    preemption: existing?.preemption,
  }
  return { ...state, calls: { ...state.calls, [call.callId]: next } }
}

export function consoleReducer(state: ConsoleState, action: ConsoleAction): ConsoleState {
  switch (action.type) {
    case 'bootstrap': {
      const users: Record<string, User> = {}
      for (const u of action.users) users[u.id] = u
      const groups: Record<string, Group> = {}
      for (const g of action.groups) groups[g.id] = g
      const activeAlerts = action.alerts.filter((a) => a.status === 'active').sort(byNewest)
      const archivedAlerts = action.alerts
        .filter((a) => a.status === 'acknowledged')
        .sort(byNewest)
      return {
        ...state,
        me: action.me,
        users,
        groups,
        affiliations: action.affiliations,
        activeAlerts,
        archivedAlerts,
        eventLog: log(state, action.now, 'session.connected', 'Roster synchronized'),
      }
    }
    case 'socketStatus':
      return { ...state, connection: action.status }
    case 'frame':
      return reduceFrame(state, action.now, action.frame)
    case 'callSnapshot':
      return reduceSnapshot(state, action.call)
    case 'reset':
      return { ...initialConsoleState }
  }
}

function byNewest(a: Alert, b: Alert): number {
  return Date.parse(b.createdAt) - Date.parse(a.createdAt)
}

// ---- selectors (pure views over the state) ----

export function activeCallList(state: ConsoleState): CallState[] {
  return Object.values(state.calls).sort((a, b) => a.callId.localeCompare(b.callId))
}

export function onlineCount(state: ConsoleState): number {
  return Object.values(state.presence).filter((s) => s === 'online').length
}

export function isAffiliated(state: ConsoleState, userId: string, groupId: string): boolean {
  return state.affiliations.some(
    (a) => a.userId === userId && a.groupId === groupId && a.state === 'affiliated',
  )
}

export function groupMemberIds(state: ConsoleState, groupId: string): string[] {
  return state.affiliations
    .filter((a) => a.groupId === groupId && a.state === 'affiliated')
    .map((a) => a.userId)
}

export function hasEmergency(state: ConsoleState): boolean {
  return (
    state.activeAlerts.length > 0 || Object.values(state.calls).some((c) => c.emergency)
  )
}

/** Whether the user is speaking or on any live call — drives roster chips. */
export function userCallRole(state: ConsoleState, userId: string): 'speaking' | 'in-call' | null {
  for (const call of Object.values(state.calls)) {
    if (call.speaker === userId) return 'speaking'
    if (call.participants.includes(userId)) return 'in-call'
  }
  return null
}
