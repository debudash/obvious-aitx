// TypeScript mirror of `mcptt/server/internal/protocol` — the WSS JSON wire
// contract shared with the Go server — plus the REST DTO shapes from
// `internal/api`. Field names must match the server exactly; when the Go
// types change, this file changes with them.

// ---- REST DTOs (api/dto.go, dispatch_handlers.go, emergency_handlers.go) ----

export type Role = 'dispatcher' | 'supervisor' | 'field'

export interface User {
  id: string
  username: string
  displayName: string
  role: Role
  priority: number
  functionalAlias?: string
  /** RFC3339 */
  createdAt: string
}

export interface Group {
  id: string
  name: string
  description?: string
  createdBy: string
  /** RFC3339 */
  createdAt: string
}

export type AffiliationState = 'affiliated' | 'deaffiliated'

export interface Affiliation {
  userId: string
  groupId: string
  state: AffiliationState
  /** RFC3339 */
  changedAt: string
}

export type AlertKind = 'emergency' | 'imminent_peril'
export type AlertStatus = 'active' | 'acknowledged'

export interface Alert {
  id: string
  userId: string
  callId?: string
  kind: AlertKind
  lat?: number
  lon?: number
  note?: string
  status: AlertStatus
  acknowledgedBy?: string
  /** RFC3339 */
  createdAt: string
}

/** One floor arbitration decision (internal/floor FloorDecision JSON). */
export interface FloorDecision {
  userId: string
  outcome: 'granted' | 'queued' | 'denied'
  priority: number
  emergency?: boolean
  token?: string
  queuePosition?: number
  reason?: string
  preemptedUserId?: string
}

export type CallKind = 'group' | 'private' | 'broadcast'

/** Spec priority ladder: dispatch 10, emergency 9, supervisors 7–8, routine 4–6, ambient 1–3. */
export type PriorityTier = 'ambient' | 'normal' | 'supervisor' | 'emergency' | 'dispatcher'

export function priorityTier(priority: number): PriorityTier {
  if (priority >= 10) return 'dispatcher'
  if (priority === 9) return 'emergency'
  if (priority >= 7) return 'supervisor'
  if (priority >= 4) return 'normal'
  return 'ambient'
}

/** Wire form of a call snapshot (dispatch_handlers.go sessionDTO). */
export interface SessionInfo {
  callId: string
  kind: CallKind
  groupId?: string
  floorControl: boolean
  emergency: boolean
  imminentPeril: boolean
  participants: string[]
  speaker?: string
  /** RFC3339 */
  speakerSince?: string
  queue: string[]
}

export interface LoginResponse {
  user: User
  token: string
}

// ---- WSS frames (protocol.go) — discriminated on `type` ----

export interface FloorRequestFrame {
  type: 'FloorRequest'
  callId: string
  userId: string
  priority: number
  emergency: boolean
}

export interface FloorGrantedFrame {
  type: 'FloorGranted'
  callId: string
  userId: string
  queue: string[]
}

export interface FloorDeniedFrame {
  type: 'FloorDenied'
  callId: string
  userId: string
  reason: string
  queuePosition: number
}

export interface FloorReleasedFrame {
  type: 'FloorReleased'
  callId: string
  userId: string
}

export interface FloorPreemptedFrame {
  type: 'FloorPreempted'
  callId: string
  by: string
  emergency: boolean
}

export interface FloorRevokeFrame {
  type: 'FloorRevoke'
  callId: string
  by: string
  reason: string
}

export interface CallStartFrame {
  type: 'CallStart'
  callId: string
  groupId?: string
  kind: CallKind
  initiatorId: string
}

export interface CallJoinedFrame {
  type: 'CallJoined'
  callId: string
  userId: string
}

export interface CallEndedFrame {
  type: 'CallEnded'
  callId: string
  by: string
}

export interface ParticipantRemovedFrame {
  type: 'ParticipantRemoved'
  callId: string
  userId: string
  by: string
}

export interface EmergencyAlertFrame {
  type: 'EmergencyAlert'
  alertId: string
  userId: string
  callId?: string
  lat?: number
  lon?: number
  emergency: boolean
  imminentPeril?: boolean
  note?: string
}

export interface EmergencyAlertAckFrame {
  type: 'EmergencyAlertAck'
  alertId: string
  acknowledgedBy: string
}

export interface PresenceUpdateFrame {
  type: 'PresenceUpdate'
  userId: string
  state: 'online' | 'offline'
  /** unix millis */
  at: number
}

export interface AffiliationChangedFrame {
  type: 'AffiliationChanged'
  userId: string
  groupId: string
  state: AffiliationState
  /** unix millis */
  at: number
}

export interface MediaAnswerFrame {
  type: 'MediaAnswer'
  callId: string
  sdp?: string
  err?: string
}

export type ServerFrame =
  | FloorGrantedFrame
  | FloorDeniedFrame
  | FloorReleasedFrame
  | FloorPreemptedFrame
  | CallStartFrame
  | CallJoinedFrame
  | CallEndedFrame
  | ParticipantRemovedFrame
  | EmergencyAlertFrame
  | EmergencyAlertAckFrame
  | PresenceUpdateFrame
  | AffiliationChangedFrame
  | MediaAnswerFrame

const FRAME_TYPES: ReadonlySet<string> = new Set([
  'FloorGranted',
  'FloorDenied',
  'FloorReleased',
  'FloorPreempted',
  'CallStart',
  'CallJoined',
  'CallEnded',
  'ParticipantRemoved',
  'EmergencyAlert',
  'EmergencyAlertAck',
  'PresenceUpdate',
  'AffiliationChanged',
  'MediaAnswer',
])

/**
 * Parse one WSS text frame. Returns null for unknown, forward-incompatible
 * types or malformed JSON — the server's own drop-unknown-types posture,
 * mirrored client-side so one bad frame never breaks the console.
 */
export function parseFrame(raw: string): ServerFrame | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (typeof parsed !== 'object' || parsed === null) return null
  const type = (parsed as { type?: unknown }).type
  if (typeof type !== 'string' || !FRAME_TYPES.has(type)) return null
  return parsed as ServerFrame
}
