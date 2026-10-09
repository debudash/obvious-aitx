// State-layer unit tests: the full floor lifecycle (grant, queue, deny,
// pre-empt, release, auto-grant), emergency alert + acknowledgement, presence,
// affiliations, and the selectors — all driven purely through the reducer with
// frames, no network.
import { describe, expect, it } from 'vitest'
import {
  activeCallList,
  consoleReducer,
  groupMemberIds,
  hasEmergency,
  initialConsoleState,
  isAffiliated,
  onlineCount,
  userCallRole,
  EVENT_LOG_CAP,
  type ConsoleState,
} from './consoleStore'
import type { ServerFrame, User, Group, Affiliation, Alert } from '../protocol/messages'

const T0 = 1_700_000_000_000

function frame(f: ServerFrame, now = T0): { type: 'frame'; now: number; frame: ServerFrame } {
  return { type: 'frame', now, frame: f }
}

function bootstrapped(overrides?: Partial<ConsoleState>): ConsoleState {
  const me: User = {
    id: 'u-disp',
    username: 'dispatcher_1',
    displayName: 'Dispatch One',
    role: 'dispatcher',
    priority: 10,
    createdAt: '2026-01-01T00:00:00Z',
  }
  const bravo: User = {
    id: 'u-bravo',
    username: 'bravo_2',
    displayName: 'Bravo 2',
    role: 'field',
    priority: 4,
    createdAt: '2026-01-01T00:00:00Z',
  }
  const charlie: User = {
    id: 'u-charlie',
    username: 'charlie_3',
    displayName: 'Charlie 3',
    role: 'field',
    priority: 5,
    createdAt: '2026-01-01T00:00:00Z',
  }
  const group: Group = {
    id: 'g-tac1',
    name: 'TAC-1',
    description: 'Tactical channel 1',
    createdBy: 'u-disp',
    createdAt: '2026-01-01T00:00:00Z',
  }
  const affiliations: Affiliation[] = [
    { userId: 'u-disp', groupId: 'g-tac1', state: 'affiliated', changedAt: '2026-01-01T00:00:00Z' },
    { userId: 'u-bravo', groupId: 'g-tac1', state: 'affiliated', changedAt: '2026-01-01T00:00:00Z' },
    { userId: 'u-charlie', groupId: 'g-tac1', state: 'affiliated', changedAt: '2026-01-01T00:00:00Z' },
  ]
  return consoleReducer(
    { ...initialConsoleState, ...overrides },
    {
      type: 'bootstrap',
      now: T0,
      me,
      users: [me, bravo, charlie],
      groups: [group],
      affiliations,
      alerts: [],
    },
  )
}

describe('bootstrap', () => {
  it('populates users, groups, affiliations and logs the sync', () => {
    const s = bootstrapped()
    expect(s.me?.username).toBe('dispatcher_1')
    expect(Object.keys(s.users)).toHaveLength(3)
    expect(s.groups['g-tac1']?.name).toBe('TAC-1')
    expect(s.affiliations).toHaveLength(3)
    expect(s.eventLog[0]?.kind).toBe('session.connected')
  })

  it('sorts active and archived alerts from the snapshot', () => {
    const alerts: Alert[] = [
      { id: 'a1', userId: 'u-bravo', kind: 'emergency', status: 'acknowledged', createdAt: '2026-01-02T00:00:00Z' },
      { id: 'a2', userId: 'u-charlie', kind: 'emergency', status: 'active', createdAt: '2026-01-03T00:00:00Z' },
    ]
    const s = consoleReducer(initialConsoleState, {
      type: 'bootstrap',
      now: T0,
      me: {
        id: 'u-disp',
        username: 'dispatcher_1',
        displayName: 'D',
        role: 'dispatcher',
        priority: 10,
        createdAt: '',
      },
      users: [],
      groups: [],
      affiliations: [],
      alerts,
    })
    expect(s.activeAlerts.map((a) => a.id)).toEqual(['a2'])
    expect(s.archivedAlerts.map((a) => a.id)).toEqual(['a1'])
  })
})

describe('floor lifecycle', () => {
  it('CallStart creates a call with the initiator as first participant', () => {
    const s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    expect(s.calls['c-1']?.participants).toEqual(['u-bravo'])
    expect(s.calls['c-1']?.kind).toBe('group')
  })

  it('FloorGranted sets the speaker, stamps the burst start, and publishes the queue', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({
      type: 'FloorGranted', callId: 'c-1', userId: 'u-bravo', queue: ['u-charlie'],
    }, T0 + 1000))
    expect(s.calls['c-1']?.speaker).toBe('u-bravo')
    expect(s.calls['c-1']?.speakerSince).toBe(T0 + 1000)
    expect(s.calls['c-1']?.queue).toEqual(['u-charlie'])
  })

  it('FloorDenied with a queue position records a queued state for that user', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({
      type: 'FloorDenied', callId: 'c-1', userId: 'u-charlie', reason: 'busy', queuePosition: 1,
    }))
    expect(s.denials['u-charlie']).toMatchObject({ reason: 'busy', queuePosition: 1 })
  })

  it('FloorDenied without a position is a hard denial with the reason', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({
      type: 'FloorDenied', callId: 'c-1', userId: 'u-charlie', reason: 'not_affiliated', queuePosition: 0,
    }))
    expect(s.denials['u-charlie']?.reason).toBe('not_affiliated')
    expect(s.denials['u-charlie']?.queuePosition).toBeUndefined()
  })

  it('a later grant for the same user clears their denial', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({
      type: 'FloorDenied', callId: 'c-1', userId: 'u-charlie', reason: 'busy', queuePosition: 1,
    }))
    s = consoleReducer(s, frame({
      type: 'FloorGranted', callId: 'c-1', userId: 'u-charlie', queue: [],
    }))
    expect(s.denials['u-charlie']).toBeUndefined()
  })

  it('FloorReleased clears only the releasing speaker', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({ type: 'FloorGranted', callId: 'c-1', userId: 'u-bravo', queue: [] }))
    s = consoleReducer(s, frame({ type: 'FloorReleased', callId: 'c-1', userId: 'u-bravo' }))
    expect(s.calls['c-1']?.speaker).toBeUndefined()
  })

  it('FloorPreempted clears the speaker and records who took the floor', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({ type: 'FloorGranted', callId: 'c-1', userId: 'u-bravo', queue: [] }))
    s = consoleReducer(s, frame({
      type: 'FloorPreempted', callId: 'c-1', by: 'u-charlie', emergency: true,
    }, T0 + 5000))
    expect(s.calls['c-1']?.speaker).toBeUndefined()
    expect(s.calls['c-1']?.preemption).toEqual({ by: 'u-charlie', emergency: true, at: T0 + 5000 })
    expect(s.eventLog[0]?.text).toContain('EMERGENCY')
  })

  it('a grant after pre-emption auto-grants the queue head (release -> grant flow)', () => {
    // Release+auto-grant arrives as a FloorGranted with the queue head; the
    // reducer must switch the speaker in one transition.
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({ type: 'FloorGranted', callId: 'c-1', userId: 'u-bravo', queue: ['u-charlie'] }))
    s = consoleReducer(s, frame({ type: 'FloorReleased', callId: 'c-1', userId: 'u-bravo' }))
    s = consoleReducer(s, frame({ type: 'FloorGranted', callId: 'c-1', userId: 'u-charlie', queue: [] }))
    expect(s.calls['c-1']?.speaker).toBe('u-charlie')
  })
})

describe('emergency alerts', () => {
  it('EmergencyAlert adds an active alert, flags the call, and logs location', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({
      type: 'EmergencyAlert', alertId: 'a-1', userId: 'u-bravo', callId: 'c-1',
      lat: 47.6205, lon: -122.3493, emergency: true,
    }))
    expect(s.activeAlerts).toHaveLength(1)
    expect(s.activeAlerts[0]).toMatchObject({ id: 'a-1', lat: 47.6205, lon: -122.3493, status: 'active' })
    expect(s.calls['c-1']?.emergency).toBe(true)
    expect(s.eventLog[0]?.text).toContain('47.6205')
  })

  it('duplicate alert ids do not duplicate the active alert', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'EmergencyAlert', alertId: 'a-1', userId: 'u-bravo', emergency: true,
    }))
    s = consoleReducer(s, frame({
      type: 'EmergencyAlert', alertId: 'a-1', userId: 'u-bravo', emergency: true,
    }))
    expect(s.activeAlerts).toHaveLength(1)
  })

  it('acknowledgement moves the alert to the archive with the acknowledger', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'EmergencyAlert', alertId: 'a-1', userId: 'u-bravo', emergency: true,
    }))
    s = consoleReducer(s, frame({ type: 'EmergencyAlertAck', alertId: 'a-1', acknowledgedBy: 'u-disp' }))
    expect(s.activeAlerts).toHaveLength(0)
    expect(s.archivedAlerts[0]).toMatchObject({ id: 'a-1', acknowledgedBy: 'u-disp', status: 'acknowledged' })
  })

  it('imminent peril is recorded as a distinct kind', () => {
    const s = consoleReducer(bootstrapped(), frame({
      type: 'EmergencyAlert', alertId: 'a-2', userId: 'u-bravo', emergency: false, imminentPeril: true,
    }))
    expect(s.activeAlerts[0]?.kind).toBe('imminent_peril')
  })
})

describe('presence and affiliations', () => {
  it('PresenceUpdate tracks user states and onlineCount', () => {
    let s = consoleReducer(bootstrapped(), frame({ type: 'PresenceUpdate', userId: 'u-bravo', state: 'online', at: T0 }))
    expect(onlineCount(s)).toBe(1)
    s = consoleReducer(s, frame({ type: 'PresenceUpdate', userId: 'u-bravo', state: 'offline', at: T0 + 1 }))
    expect(onlineCount(s)).toBe(0)
  })

  it('AffiliationChanged appends new rows and updates existing ones', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'AffiliationChanged', userId: 'u-charlie', groupId: 'g-tac1', state: 'deaffiliated', at: T0,
    }))
    expect(isAffiliated(s, 'u-charlie', 'g-tac1')).toBe(false)
    s = consoleReducer(s, frame({
      type: 'AffiliationChanged', userId: 'u-charlie', groupId: 'g-tac1', state: 'affiliated', at: T0 + 1,
    }))
    expect(isAffiliated(s, 'u-charlie', 'g-tac1')).toBe(true)
    expect(groupMemberIds(s, 'g-tac1')).toHaveLength(3)
  })
})

describe('call teardown', () => {
  it('ParticipantRemoved drops the user from participants, queue, and speaker', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({ type: 'CallJoined', callId: 'c-1', userId: 'u-charlie' }))
    s = consoleReducer(s, frame({ type: 'FloorGranted', callId: 'c-1', userId: 'u-charlie', queue: ['u-bravo'] }))
    s = consoleReducer(s, frame({ type: 'ParticipantRemoved', callId: 'c-1', userId: 'u-charlie', by: 'u-disp' }))
    const c = s.calls['c-1']!
    expect(c.participants).toEqual(['u-bravo'])
    expect(c.speaker).toBeUndefined()
    expect(c.queue).toEqual(['u-bravo'])
  })

  it('CallEnded removes the call', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({ type: 'CallEnded', callId: 'c-1', by: 'u-disp' }))
    expect(s.calls['c-1']).toBeUndefined()
  })
})

describe('event log', () => {
  it('is capped at EVENT_LOG_CAP entries, newest first', () => {
    let s = bootstrapped()
    for (let i = 0; i < EVENT_LOG_CAP + 50; i++) {
      s = consoleReducer(s, frame({
        type: 'CallStart', callId: `c-${i}`, groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
      }, T0 + i))
    }
    expect(s.eventLog).toHaveLength(EVENT_LOG_CAP)
    // Newest first: the latest frame's timestamp is on top.
    expect(s.eventLog[0]?.at).toBe(T0 + EVENT_LOG_CAP + 49)
  })
})

describe('selectors', () => {
  it('userCallRole distinguishes speaking from in-call', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'CallStart', callId: 'c-1', groupId: 'g-tac1', kind: 'group', initiatorId: 'u-bravo',
    }))
    s = consoleReducer(s, frame({ type: 'CallJoined', callId: 'c-1', userId: 'u-charlie' }))
    s = consoleReducer(s, frame({ type: 'FloorGranted', callId: 'c-1', userId: 'u-bravo', queue: [] }))
    expect(userCallRole(s, 'u-bravo')).toBe('speaking')
    expect(userCallRole(s, 'u-charlie')).toBe('in-call')
    expect(userCallRole(s, 'u-disp')).toBeNull()
  })

  it('activeCallList sorts by callId for stable rendering', () => {
    let s = consoleReducer(bootstrapped(), frame({ type: 'CallStart', callId: 'c-b', kind: 'group', initiatorId: 'u-bravo' }))
    s = consoleReducer(s, frame({ type: 'CallStart', callId: 'c-a', kind: 'broadcast', initiatorId: 'u-disp' }))
    expect(activeCallList(s).map((c) => c.callId)).toEqual(['c-a', 'c-b'])
  })

  it('hasEmergency reflects live emergency calls and active alerts', () => {
    let s = consoleReducer(bootstrapped(), frame({
      type: 'EmergencyAlert', alertId: 'a-1', userId: 'u-bravo', emergency: true,
    }))
    expect(hasEmergency(s)).toBe(true)
    s = consoleReducer(s, frame({ type: 'EmergencyAlertAck', alertId: 'a-1', acknowledgedBy: 'u-disp' }))
    expect(hasEmergency(s)).toBe(false)
  })
})
