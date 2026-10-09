// Roster panel — the left rail. Every user with presence, priority tier,
// functional alias, and affiliation counts; live call role chips; group
// emergency + affiliation actions. Mirrors mockup state 1.
import { useMemo, useState } from 'react'
import type { User } from '../protocol/messages'
import { CAPABILITIES } from '../api/capabilities'
import {
  isAffiliated,
  onlineCount,
  userCallRole,
  type ConsoleState,
} from '../state/consoleStore'
import type { ConsoleActions } from '../state/session'
import { EmptyState, Panel, PriorityBadge, StatusDot, btnDanger, btnPrimary } from './ui'

function userLabel(u: User): string {
  return u.functionalAlias ? `${u.displayName} · ${u.functionalAlias}` : u.displayName
}

function RosterRow(props: { user: User; state: ConsoleState }) {
  const { user, state } = props
  const role = userCallRole(state, user.id)
  const groups = state.affiliations.filter(
    (a) => a.userId === user.id && a.state === 'affiliated',
  ).length
  const isMe = user.id === state.me?.id
  return (
    <li className="flex items-center justify-between gap-2 rounded px-2 py-1.5 hover:bg-slate-800/60">
      <span className="flex min-w-0 items-center gap-2">
        <StatusDot state={state.presence[user.id] ?? 'offline'} />
        <span className="min-w-0">
          <span className="block truncate text-sm text-slate-100">{userLabel(user)}</span>
          <span className="block text-xs text-slate-500">
            {groups} affiliated
            {role === 'speaking' ? ' · speaking' : role === 'in-call' ? ' · in call' : ''}
          </span>
        </span>
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <PriorityBadge priority={user.priority} />
        <button
          type="button"
          className={`${btnPrimary} px-2 py-0.5 text-xs`}
          disabled={isMe || !CAPABILITIES.privateCall}
          title={
            isMe
              ? 'That is you'
              : CAPABILITIES.privateCall
                ? `Private call ${user.displayName}`
                : 'Private calls wait on server call-start routes'
          }
        >
          Call
        </button>
      </span>
    </li>
  )
}

export function Roster(props: {
  state: ConsoleState
  actions: ConsoleActions
  onEmergencyToGroup: (groupId: string) => void
}) {
  const { state, actions } = props
  const [query, setQuery] = useState('')
  const users = useMemo(() => {
    const list = Object.values(state.users)
    if (!query.trim()) return list
    const q = query.trim().toLowerCase()
    return list.filter(
      (u) =>
        u.username.toLowerCase().includes(q) ||
        u.displayName.toLowerCase().includes(q) ||
        (u.functionalAlias ?? '').toLowerCase().includes(q),
    )
  }, [state.users, query])

  const groups = Object.values(state.groups)

  return (
    <Panel title={`Roster · ${onlineCount(state)} online`}>
      <input
        type="search"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Filter roster…"
        aria-label="Filter roster"
        className="mb-3 w-full rounded border border-slate-700 bg-slate-950 px-3 py-1.5 text-sm text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400"
      />
      {users.length === 0 ? (
        <EmptyState>No roster entries match.</EmptyState>
      ) : (
        <ul className="max-h-72 space-y-0.5 overflow-y-auto" aria-label="Roster list">
          {users.map((u) => (
            <RosterRow key={u.id} user={u} state={state} />
          ))}
        </ul>
      )}

      <h3 className="mt-4 mb-2 text-xs font-semibold uppercase tracking-widest text-slate-500">
        Groups
      </h3>
      {groups.length === 0 ? (
        <EmptyState>No groups configured.</EmptyState>
      ) : (
        <ul className="space-y-1" aria-label="Group list">
          {groups.map((g) => {
            const members = state.affiliations.filter(
              (a) => a.groupId === g.id && a.state === 'affiliated',
            ).length
            const mine = state.me ? isAffiliated(state, state.me.id, g.id) : false
            return (
              <li
                key={g.id}
                className="flex items-center justify-between gap-2 rounded px-2 py-1.5 hover:bg-slate-800/60"
              >
                <span className="min-w-0">
                  <span className="block truncate text-sm text-slate-100">{g.name}</span>
                  <span className="block text-xs text-slate-500">
                    {members} affiliated{mine ? ' · you are affiliated' : ''}
                  </span>
                </span>
                <span className="flex shrink-0 items-center gap-1.5">
                  <button
                    type="button"
                    className={`${btnPrimary} px-2 py-0.5 text-xs`}
                    onClick={() => props.onEmergencyToGroup(g.id)}
                    title={`Emergency call on ${g.name}`}
                  >
                    Emergency
                  </button>
                  <button
                    type="button"
                    className={`${btnDanger} px-2 py-0.5 text-xs`}
                    onClick={() => void actions.deaffiliate(g.id, state.me?.id)}
                    disabled={!mine}
                    title={mine ? `Deaffiliate from ${g.name}` : 'Not affiliated'}
                  >
                    Leave
                  </button>
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}
