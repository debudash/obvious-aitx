// Console shell: login gate, header with connection status, Ops and Admin
// tabs, the three-column operations layout from the mockup, and the global
// error toast. Emergency actions arm behind an explicit confirm step —
// a dispatcher must never raise an emergency by accident.
import { useState } from 'react'
import { CAPABILITY_NOTE, CAPABILITIES } from './api/capabilities'
import { hasEmergency } from './state/consoleStore'
import { useConsoleSession } from './state/session'
import { Login } from './components/Login'
import { Roster } from './components/Roster'
import { CallPanel } from './components/CallPanel'
import { EmergencyRail } from './components/EmergencyRail'
import { ActivityLog } from './components/ActivityLog'
import { AuditView, GroupsAdmin, PriorityAdmin } from './components/AdminPanels'
import { ErrorToast, PriorityBadge, btnDanger } from './components/ui'

const TOKEN_KEY = 'mcptt.dispatch.token'

function connectionChip(status: 'connecting' | 'open' | 'closed') {
  const map = {
    open: { label: 'LIVE', cls: 'border-emerald-700 bg-emerald-950 text-emerald-300' },
    connecting: { label: 'CONNECTING', cls: 'border-amber-700 bg-amber-950 text-amber-300' },
    closed: { label: 'OFFLINE', cls: 'border-slate-700 bg-slate-900 text-slate-400' },
  } as const
  const { label, cls } = map[status]
  return <span className={`rounded border px-2 py-0.5 font-mono text-xs ${cls}`}>{label}</span>
}

export default function App() {
  const [token, setToken] = useState<string | null>(() => sessionStorage.getItem(TOKEN_KEY))
  const session = useConsoleSession(token)
  const { state, actions, error, clearError, refresh } = session
  const [tab, setTab] = useState<'ops' | 'admin'>('ops')
  const [armed, setArmed] = useState<{ groupId?: string; label: string } | null>(null)
  // Admin panels catch their own API errors and report them here so the same
  // toast carries every failure in the window.
  const [adminError, setAdminError] = useState<string | null>(null)

  function signIn(t: string) {
    sessionStorage.setItem(TOKEN_KEY, t)
    setToken(t)
  }

  function signOut() {
    sessionStorage.removeItem(TOKEN_KEY)
    setToken(null)
    setArmed(null)
  }

  function fireEmergency(target?: { groupId?: string; label: string }) {
    if (target) {
      setArmed(target)
      return
    }
    // No explicit target: default to the dispatcher's first affiliated group.
    const mine = state.me
      ? state.affiliations.find((a) => a.userId === state.me!.id && a.state === 'affiliated')
      : undefined
    if (!mine) {
      setAdminError('You have no affiliated group to raise an emergency on — affiliate first.')
      return
    }
    setArmed({ groupId: mine.groupId, label: state.groups[mine.groupId]?.name ?? mine.groupId })
  }

  if (!token) {
    return <Login onToken={signIn} />
  }

  return (
    <div className="min-h-dvh bg-slate-950 text-slate-100">
      <header
        className={`flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3 ${
          hasEmergency(state) ? 'border-red-800 bg-red-950/40' : 'border-slate-800 bg-slate-900/60'
        }`}
      >
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-semibold tracking-tight">MCPTT Dispatch</h1>
          {connectionChip(state.connection)}
          {hasEmergency(state) && (
            <span className="animate-pulse rounded border border-red-600 bg-red-900 px-2 py-0.5 font-mono text-xs font-bold text-red-100">
              EMERGENCY ACTIVE
            </span>
          )}
        </div>
        <div className="flex items-center gap-3">
          {state.me && (
            <span className="flex items-center gap-2 text-sm text-slate-300">
              {state.me.displayName}
              <PriorityBadge priority={state.me.priority} />
            </span>
          )}
          <nav role="tablist" aria-label="Console views" className="flex gap-1">
            <button
              type="button"
              role="tab"
              aria-selected={tab === 'ops'}
              onClick={() => setTab('ops')}
              className={`rounded px-3 py-1 text-sm ${
                tab === 'ops' ? 'bg-sky-900 text-sky-100' : 'text-slate-400 hover:bg-slate-800'
              } focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400`}
            >
              Operations
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={tab === 'admin'}
              onClick={() => setTab('admin')}
              className={`rounded px-3 py-1 text-sm ${
                tab === 'admin' ? 'bg-sky-900 text-sky-100' : 'text-slate-400 hover:bg-slate-800'
              } focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400`}
            >
              Admin
            </button>
          </nav>
          <button type="button" onClick={signOut} className={btnDanger}>
            Sign out
          </button>
        </div>
      </header>

      {armed && (
        <div
          role="alertdialog"
          aria-label="Confirm emergency"
          className="flex flex-wrap items-center justify-between gap-3 border-b border-red-700 bg-red-950/70 px-4 py-2"
        >
          <span className="text-sm text-red-100">
            Raise an EMERGENCY on {armed.label}? This pre-empts active floors and alerts every
            dispatcher.
          </span>
          <span className="flex gap-2">
            <button
              type="button"
              className="rounded border border-red-500 bg-red-800 px-3 py-1 text-sm font-semibold text-red-50 hover:bg-red-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-red-300"
              onClick={() => {
                const groupId = armed.groupId
                setArmed(null)
                void actions.emergencyCall(groupId ? { groupId } : {})
              }}
              autoFocus
            >
              Confirm emergency
            </button>
            <button
              type="button"
              className="rounded border border-slate-600 bg-slate-800 px-3 py-1 text-sm text-slate-200 hover:bg-slate-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400"
              onClick={() => setArmed(null)}
            >
              Cancel
            </button>
          </span>
        </div>
      )}

      <main className="mx-auto max-w-7xl px-4 py-4">
        {tab === 'ops' ? (
          <div className="space-y-4">
            {hasEmergency(state) && (
              <p className="rounded border border-red-800 bg-red-950/60 px-3 py-2 text-sm text-red-200">
                Emergency in progress — acknowledge it in the Emergency panel.
              </p>
            )}
            <div className="grid gap-4 lg:grid-cols-3">
              <Roster
                state={state}
                actions={actions}
                onEmergencyToGroup={(groupId) =>
                  fireEmergency({
                    groupId,
                    label: state.groups[groupId]?.name ?? groupId,
                  })
                }
              />
              <CallPanel
                state={state}
                actions={actions}
                onEmergency={() => fireEmergency()}
              />
              <EmergencyRail state={state} actions={actions} />
            </div>
            <ActivityLog state={state} />
            {!CAPABILITIES.floorRequest && (
              <p className="rounded border border-slate-800 bg-slate-900/60 px-3 py-2 text-xs text-slate-500">
                {CAPABILITY_NOTE}
              </p>
            )}
          </div>
        ) : (
          <div className="space-y-4">
            <GroupsAdmin
              state={state}
              token={token}
              refresh={() => void refresh()}
              onError={setAdminError}
            />
            <PriorityAdmin
              state={state}
              token={token}
              refresh={() => void refresh()}
              onError={setAdminError}
            />
            <AuditView />
          </div>
        )}
      </main>

      {error && <ErrorToast message={error} onDismiss={clearError} />}
      {adminError && (
        <ErrorToast message={adminError} onDismiss={() => setAdminError(null)} />
      )}
    </div>
  )
}
