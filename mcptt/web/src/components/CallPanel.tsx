// Center panel — active calls and call initiation. Mirrors mockup states 1
// (idle: three initiation affordances) and 2 (live call: talker, burst timer,
// queue, dispatch controls). Disabled controls carry an honest reason.
import { useEffect, useState } from 'react'
import { CAPABILITIES, CAPABILITY_NOTE } from '../api/capabilities'
import { kindLabel, type CallState, type ConsoleState } from '../state/consoleStore'
import type { ConsoleActions } from '../state/session'
import { mmss } from '../lib/format'
import { EmptyState, Panel, PriorityBadge, btnDanger, btnDefault, btnPrimary } from './ui'

/** Re-render every `ms` so burst timers tick. Mounts one interval per call. */
function useTicker(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms])
  return now
}

function CallCard(props: { call: CallState; state: ConsoleState; actions: ConsoleActions }) {
  const { call, state, actions } = props
  const now = useTicker()
  const group = call.groupId ? state.groups[call.groupId] : undefined
  const title = group ? group.name : kindLabel(call.kind)

  return (
    <article
      className={`rounded-lg border p-4 ${
        call.emergency ? 'border-red-700 bg-red-950/40' : 'border-slate-800 bg-slate-900/60'
      }`}
      aria-label={`${kindLabel(call.kind)} call ${title}`}
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-slate-100">
          {kindLabel(call.kind)} · {title}
          {call.emergency && (
            <span className="ml-2 rounded border border-red-600 bg-red-900 px-1.5 py-0.5 text-xs font-bold text-red-100">
              {call.imminentPeril ? 'IMMINENT PERIL' : 'EMERGENCY'}
            </span>
          )}
        </h3>
        <span className="text-xs text-slate-500">{call.participants.length} on call</span>
      </header>

      {call.preemption && (
        <p role="status" className="mt-2 rounded border border-orange-700 bg-orange-950/60 px-3 py-1.5 text-xs text-orange-200">
          Floor pre-empted by {state.users[call.preemption.by]?.displayName ?? call.preemption.by}
          {call.preemption.emergency ? ' — emergency' : ''}
        </p>
      )}

      {call.speaker ? (
        <p className="mt-3 text-sm text-slate-100" aria-live="polite">
          <span className="mr-2 inline-block size-2 animate-pulse rounded-full bg-emerald-400" aria-hidden />
          {state.users[call.speaker]?.displayName ?? call.speaker} — talk burst{' '}
          <span className="font-mono">{mmss(now - (call.speakerSince ?? now))}</span>
        </p>
      ) : (
        <p className="mt-3 text-sm text-slate-400">Floor idle — nobody transmitting.</p>
      )}

      <div className="mt-3">
        <h4 className="text-xs font-semibold uppercase tracking-widest text-slate-500">Queue</h4>
        {call.queue.length === 0 ? (
          <EmptyState>Nobody waiting to speak.</EmptyState>
        ) : (
          <ol className="mt-1 space-y-1" aria-label="Floor queue">
            {call.queue.map((uid, i) => (
              <li key={uid} className="flex items-center justify-between gap-2 text-sm text-slate-200">
                <span>
                  <span className="mr-2 font-mono text-slate-500">#{i + 1}</span>
                  {state.users[uid]?.displayName ?? uid}
                </span>
                <PriorityBadge priority={state.users[uid]?.priority ?? 0} />
              </li>
            ))}
          </ol>
        )}
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <button
          type="button"
          className={btnPrimary}
          disabled={!CAPABILITIES.floorRequest}
          title={
            CAPABILITIES.floorRequest
              ? 'Request the floor at P10 (net control)'
              : 'Server does not expose client floor-request routes yet'
          }
        >
          Talk (P10)
        </button>
        <button
          type="button"
          className={btnDanger}
          onClick={() => void actions.revokeFloor(call.callId)}
          disabled={!call.speaker}
          title="Strip the floor from the current talker"
        >
          Revoke floor
        </button>
        <button
          type="button"
          className={btnDanger}
          onClick={() => void actions.endCall(call.callId)}
          title="End this call for every participant"
        >
          End call
        </button>
      </div>

      <div className="mt-3">
        <h4 className="text-xs font-semibold uppercase tracking-widest text-slate-500">
          Participants
        </h4>
        <ul className="mt-1 grid gap-1 sm:grid-cols-2">
          {call.participants.map((uid) => (
            <li key={uid} className="flex items-center justify-between gap-2 rounded border border-slate-800 px-2 py-1 text-sm text-slate-200">
              <span className="min-w-0 truncate">{state.users[uid]?.displayName ?? uid}</span>
              <button
                type="button"
                className={`${btnDanger} px-2 py-0.5 text-xs`}
                onClick={() => void actions.removeParticipant(call.callId, uid)}
                title={`Remove ${state.users[uid]?.displayName ?? uid} from the call`}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      </div>
    </article>
  )
}

export function CallPanel(props: {
  state: ConsoleState
  actions: ConsoleActions
  onEmergency: () => void
}) {
  const { state, actions } = props
  const calls = Object.values(state.calls)
  const groups = Object.values(state.groups)
  const [broadcastFor, setBroadcastFor] = useState('')

  return (
    <Panel title="Calls">
      {calls.length === 0 ? (
        <div className="space-y-4">
          <EmptyState>
            Select a group or person to start a call — group, private, or broadcast.
          </EmptyState>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              className={btnDefault}
              disabled={!CAPABILITIES.groupCallStart}
              title={
                CAPABILITIES.groupCallStart
                  ? 'Start a group call'
                  : 'Server does not expose client call-start routes yet'
              }
            >
              + Group call
            </button>
            <button
              type="button"
              className={btnDefault}
              disabled={!CAPABILITIES.privateCall}
              title={
                CAPABILITIES.privateCall
                  ? 'Start a private call'
                  : 'Server does not expose private-call routes yet'
              }
            >
              + Private call
            </button>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <label className="text-sm text-slate-300" htmlFor="broadcast-group">
              Broadcast to
              <select
                id="broadcast-group"
                value={broadcastFor}
                onChange={(e) => setBroadcastFor(e.target.value)}
                className="ml-2 rounded border border-slate-700 bg-slate-950 px-2 py-1 text-sm text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400"
              >
                <option value="">Choose group…</option>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className={btnPrimary}
              disabled={!broadcastFor}
              onClick={() => {
                if (broadcastFor) void actions.startBroadcast(broadcastFor)
                setBroadcastFor('')
              }}
              title="One-to-many announcement with a receive-only floor"
            >
              + Broadcast
            </button>
          </div>
          {!CAPABILITIES.groupCallStart && (
            <p className="rounded border border-slate-800 bg-slate-900 px-3 py-2 text-xs text-slate-400">
              {CAPABILITY_NOTE}
            </p>
          )}
          <button type="button" className={btnDanger} onClick={props.onEmergency} title="Raise an emergency call on your affiliated group">
            Emergency call
          </button>
        </div>
      ) : (
        <div className="space-y-4">
          {calls.map((c) => (
            <CallCard key={c.callId} call={c} state={state} actions={actions} />
          ))}
        </div>
      )}
    </Panel>
  )
}
