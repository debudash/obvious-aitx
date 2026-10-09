// Admin tab — group admin (create/edit/delete, membership), priority admin
// (priority, role, functional alias per user), and the audit view. All REST
// calls go through the ApiClient so the bearer token is always attached.
import { useState, type ReactNode } from 'react'
import { ApiClient, ApiError } from '../api/client'
import { CAPABILITIES } from '../api/capabilities'
import { groupMemberIds, type ConsoleState } from '../state/consoleStore'
import { EmptyState, Panel, PriorityBadge, btnDanger, btnDefault, btnPrimary } from './ui'

function Field(props: { label: string; children: ReactNode }) {
  return (
    <label className="block text-sm text-slate-300">
      {props.label}
      {props.children}
    </label>
  )
}

const inputCls =
  'mt-1 w-full rounded border border-slate-700 bg-slate-950 px-3 py-1.5 text-sm text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400'

export function GroupsAdmin(props: {
  state: ConsoleState
  token: string
  refresh: () => void
  onError: (m: string) => void
}) {
  const { state } = props
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [memberPick, setMemberPick] = useState('')

  async function run(fn: (api: ApiClient) => Promise<unknown>) {
    try {
      await fn(new ApiClient(props.token))
      props.refresh()
    } catch (err) {
      props.onError(err instanceof ApiError ? err.message : String(err))
    }
  }

  const users = Object.values(state.users)
  return (
    <Panel title="Group administration">
      <form
        className="mb-4 flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (!name.trim()) return
          void run((api) => api.createGroup(name.trim(), description.trim()))
          setName('')
          setDescription('')
        }}
      >
        <Field label="Name">
          <input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Description">
          <input
            className={inputCls}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>
        <button type="submit" className={btnPrimary} disabled={!name.trim()}>
          Create group
        </button>
      </form>

      {Object.values(state.groups).length === 0 ? (
        <EmptyState>No groups yet — create the first one above.</EmptyState>
      ) : (
        <ul className="space-y-2">
          {Object.values(state.groups).map((g) => {
            const members = groupMemberIds(state, g.id)
            return (
              <li key={g.id} className="rounded border border-slate-800 p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="min-w-0">
                    <p className="text-sm font-medium text-slate-100">{g.name}</p>
                    <p className="text-xs text-slate-500">
                      {g.description || 'No description'} · {members.length} members
                    </p>
                  </span>
                  <span className="flex gap-2">
                    <button
                      type="button"
                      className={`${btnDanger} px-2 py-0.5 text-xs`}
                      onClick={() => void run((api) => api.deleteGroup(g.id))}
                    >
                      Delete
                    </button>
                  </span>
                </div>
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <select
                    aria-label={`Add member to ${g.name}`}
                    value={memberPick}
                    onChange={(e) => setMemberPick(e.target.value)}
                    className="rounded border border-slate-700 bg-slate-950 px-2 py-1 text-xs text-slate-100"
                  >
                    <option value="">Add member…</option>
                    {users
                      .filter((u) => !members.includes(u.id))
                      .map((u) => (
                        <option key={u.id} value={u.id}>
                          {u.displayName}
                        </option>
                      ))}
                  </select>
                  <button
                    type="button"
                    className={`${btnDefault} px-2 py-0.5 text-xs`}
                    disabled={!memberPick}
                    onClick={() => {
                      if (!memberPick) return
                      void run((api) => api.addMember(g.id, memberPick))
                      setMemberPick('')
                    }}
                  >
                    Add
                  </button>
                  <span className="text-xs text-slate-500">
                    Members:{' '}
                    {members.map((id) => state.users[id]?.displayName ?? id).join(', ') || 'none'}
                  </span>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

export function PriorityAdmin(props: {
  state: ConsoleState
  token: string
  refresh: () => void
  onError: (m: string) => void
}) {
  const { state } = props
  const users = Object.values(state.users)

  async function patch(id: string, body: Record<string, unknown>) {
    try {
      await new ApiClient(props.token).updateUser(id, body)
      props.refresh()
    } catch (err) {
      props.onError(err instanceof ApiError ? err.message : String(err))
    }
  }

  return (
    <Panel title="Priority administration">
      {users.length === 0 ? (
        <EmptyState>No users.</EmptyState>
      ) : (
        <ul className="space-y-1" aria-label="User priorities">
          {users.map((u) => (
            <li
              key={u.id}
              className="flex flex-wrap items-center justify-between gap-2 rounded border border-slate-800 px-3 py-2"
            >
              <span className="flex min-w-0 items-center gap-2">
                <PriorityBadge priority={u.priority} />
                <span className="min-w-0">
                  <span className="block truncate text-sm text-slate-100">{u.displayName}</span>
                  <span className="block text-xs text-slate-500">
                    {u.username} · {u.role}
                    {u.functionalAlias ? ` · ${u.functionalAlias}` : ''}
                  </span>
                </span>
              </span>
              <span className="flex items-center gap-2">
                <label className="text-xs text-slate-400" htmlFor={`prio-${u.id}`}>
                  Priority
                  <select
                    id={`prio-${u.id}`}
                    value={u.priority}
                    onChange={(e) => void patch(u.id, { priority: Number(e.target.value) })}
                    className="ml-2 rounded border border-slate-700 bg-slate-950 px-1.5 py-0.5 text-xs text-slate-100"
                  >
                    {Array.from({ length: 10 }, (_, i) => i + 1).map((p) => (
                      <option key={p} value={p}>
                        P{p}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="text-xs text-slate-400" htmlFor={`alias-${u.id}`}>
                  Alias
                  <input
                    id={`alias-${u.id}`}
                    defaultValue={u.functionalAlias ?? ''}
                    placeholder="e.g. Squad 3 Lead"
                    onBlur={(e) => {
                      const v = e.target.value.trim()
                      if (v !== (u.functionalAlias ?? '')) void patch(u.id, { functionalAlias: v })
                    }}
                    className="ml-2 w-36 rounded border border-slate-700 bg-slate-950 px-2 py-0.5 text-xs text-slate-100"
                  />
                </label>
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-xs text-slate-500">
        Ladder: P10 dispatch (pre-empts everything), P9 emergency, P7–8 supervisors, P4–6 routine,
        P1–3 ambient — configurable per user; seed data, not architecture.
      </p>
    </Panel>
  )
}

export function AuditView() {
  return (
    <Panel title="Audit log">
      {!CAPABILITIES.auditLog ? (
        <EmptyState>
          The server records audit entries, but this build exposes no audit-read route yet — the
          view will list every dispatch action here the moment the route lands.
        </EmptyState>
      ) : (
        <EmptyState>Loaded.</EmptyState>
      )}
    </Panel>
  )
}
