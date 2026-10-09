// Right rail — emergencies. Mockup state 3: emergency call banner with live
// location, voiceless alerts awaiting acknowledgement, and the archive.
import { useEffect, useState } from 'react'
import { CAPABILITIES } from '../api/capabilities'
import type { ConsoleState } from '../state/consoleStore'
import type { ConsoleActions } from '../state/session'
import { formatCoords, mapUrl, timeAgo } from '../lib/format'
import { EmptyState, Panel, btnDanger, btnPrimary } from './ui'

function AlertCard(props: {
  alert: ConsoleState['activeAlerts'][number]
  state: ConsoleState
  actions: ConsoleActions
  archived?: boolean
}) {
  const { alert, state } = props
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const user = state.users[alert.userId]
  const createdAt = Date.parse(alert.createdAt)
  return (
    <li
      className={`rounded-lg border p-3 ${
        props.archived ? 'border-slate-800 bg-slate-900/60' : 'border-red-700 bg-red-950/50'
      }`}
      aria-label={props.archived ? 'Archived alert' : 'Active alert'}
    >
      <p className="text-sm font-semibold text-slate-100">
        {props.archived ? '' : 'ALERT · '}
        {user?.displayName ?? alert.userId}
        {alert.kind === 'imminent_peril' ? ' · imminent peril' : ''}
        {props.archived ? ' · acknowledged' : ` · ${timeAgo(createdAt, now)}`}
      </p>
      <p className="mt-1 text-xs text-slate-400">
        {alert.lat !== undefined && alert.lon !== undefined ? (
          <a
            href={mapUrl(alert.lat, alert.lon)}
            target="_blank"
            rel="noreferrer"
            className="underline decoration-dotted focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400"
          >
            Location {formatCoords(alert.lat, alert.lon)}
          </a>
        ) : (
          'No location reported'
        )}
        {alert.callId ? ' · linked to the live call' : ' · voiceless alert'}
      </p>
      {alert.note && <p className="mt-1 text-xs text-slate-400">“{alert.note}”</p>}
      {!props.archived && (
        <button
          type="button"
          className={`${btnPrimary} mt-2`}
          onClick={() => void props.actions.ackAlert(alert.id)}
          title="Acknowledge to clear the alert into the archive"
        >
          Acknowledge
        </button>
      )}
      {props.archived && alert.acknowledgedBy && (
        <p className="mt-1 text-xs text-slate-500">
          Acknowledged by {state.users[alert.acknowledgedBy]?.displayName ?? alert.acknowledgedBy}
        </p>
      )}
    </li>
  )
}

export function EmergencyRail(props: { state: ConsoleState; actions: ConsoleActions }) {
  const { state, actions } = props
  const emergencyCalls = Object.values(state.calls).filter((c) => c.emergency)
  return (
    <Panel title="Emergency" danger={state.activeAlerts.length > 0 || emergencyCalls.length > 0}>
      {emergencyCalls.length === 0 && state.activeAlerts.length === 0 ? (
        <EmptyState>
          No alerts. Acknowledged alerts are archived with location and timestamps.
        </EmptyState>
      ) : (
        <div className="space-y-3">
          {emergencyCalls.map((c) => {
            const caller = c.participants[0]
            const alert = state.activeAlerts.find((a) => a.callId === c.callId)
            return (
              <div
                key={c.callId}
                role="alert"
                className="rounded-lg border border-red-600 bg-red-950/60 p-3"
              >
                <p className="text-sm font-bold text-red-100">
                  EMERGENCY CALL · {state.users[caller ?? '']?.displayName ?? caller ?? 'unknown'}
                </p>
                <p className="mt-1 text-xs text-red-200">
                  Floor pre-empted — {state.users[c.speaker ?? '']?.displayName ?? 'caller'} speaking
                </p>
                {alert?.lat !== undefined && alert?.lon !== undefined && (
                  <p className="mt-1 text-xs text-red-200">
                    Location:{' '}
                    <a
                      href={mapUrl(alert.lat, alert.lon)}
                      target="_blank"
                      rel="noreferrer"
                      className="underline decoration-dotted"
                    >
                      {formatCoords(alert.lat, alert.lon)}
                    </a>
                  </p>
                )}
                <div className="mt-2 flex gap-2">
                  {alert && (
                    <button
                      type="button"
                      className={btnPrimary}
                      onClick={() => void actions.ackAlert(alert.id)}
                      title="Acknowledge the linked alert"
                    >
                      Acknowledge
                    </button>
                  )}
                  <button
                    type="button"
                    className={btnDanger}
                    onClick={() => void actions.endCall(c.callId)}
                    title="End the emergency call"
                  >
                    End emergency
                  </button>
                </div>
              </div>
            )
          })}
          <ul className="space-y-2" aria-label="Active alerts">
            {state.activeAlerts
              .filter((a) => !a.callId || !state.calls[a.callId])
              .map((a) => (
                <AlertCard key={a.id} alert={a} state={state} actions={actions} />
              ))}
          </ul>
        </div>
      )}

      {state.archivedAlerts.length > 0 && (
        <details className="mt-4">
          <summary className="cursor-pointer text-xs font-semibold uppercase tracking-widest text-slate-500">
            Archive · {state.archivedAlerts.length}
          </summary>
          <ul className="mt-2 space-y-2" aria-label="Archived alerts">
            {state.archivedAlerts.map((a) => (
              <AlertCard key={a.id} alert={a} state={state} actions={actions} archived />
            ))}
          </ul>
        </details>
      )}
      {!CAPABILITIES.browserMedia && null}
    </Panel>
  )
}
