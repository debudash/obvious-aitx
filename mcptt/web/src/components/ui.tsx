// Small presentational primitives shared across panels. Dark operations
// aesthetic: near-black canvas, slate panels, amber/red for urgency.
import type { ReactNode } from 'react'
import { priorityTier } from '../protocol/messages'
import type { PresenceState } from '../state/consoleStore'

export function Panel(props: {
  title: string
  actions?: ReactNode
  children: ReactNode
  className?: string
  danger?: boolean
}) {
  return (
    <section
      className={`rounded-lg border ${props.danger ? 'border-red-900/70' : 'border-slate-800'} bg-slate-900/60 ${props.className ?? ''}`}
      aria-label={props.title}
    >
      <header className="flex items-center justify-between gap-2 border-b border-slate-800 px-4 py-2">
        <h2 className="text-xs font-semibold uppercase tracking-widest text-slate-400">
          {props.title}
        </h2>
        {props.actions}
      </header>
      <div className="p-4">{props.children}</div>
    </section>
  )
}

export function StatusDot({ state }: { state: PresenceState }) {
  const color = state === 'online' ? 'bg-emerald-400' : 'bg-slate-600'
  return (
    <span
      className={`inline-block size-2 rounded-full ${color}`}
      title={state}
      aria-label={`presence: ${state}`}
    />
  )
}

/** P1–P10 chip: tier-colored (P10 red = net control, P4–6 sky = routine). */
export function PriorityBadge({ priority }: { priority: number }) {
  const tier = priorityTier(priority)
  const cls =
    tier === 'dispatcher'
      ? 'border-red-700 bg-red-950 text-red-300'
      : tier === 'emergency'
        ? 'border-orange-700 bg-orange-950 text-orange-300'
        : tier === 'supervisor'
          ? 'border-amber-700 bg-amber-950 text-amber-300'
          : tier === 'normal'
            ? 'border-sky-800 bg-sky-950 text-sky-300'
            : 'border-slate-700 bg-slate-800 text-slate-400'
  return (
    <span className={`rounded border px-1.5 py-0.5 font-mono text-[10px] ${cls}`}>
      P{priority}
    </span>
  )
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <p className="text-sm text-slate-500">{children}</p>
}

export function ErrorToast({ message, onDismiss }: { message: string; onDismiss: () => void }) {
  return (
    <div
      role="alert"
      className="fixed bottom-4 left-1/2 z-50 flex w-[min(32rem,90vw)] -translate-x-1/2 items-center justify-between gap-4 rounded-lg border border-red-800 bg-red-950/95 px-4 py-3 text-sm text-red-100 shadow-lg"
    >
      <span>{message}</span>
      <button
        type="button"
        onClick={onDismiss}
        className="rounded border border-red-700 px-2 py-0.5 text-xs text-red-200 hover:bg-red-900 focus-visible:outline focus-visible:outline-2 focus-visible:outline-red-400"
      >
        Dismiss
      </button>
    </div>
  )
}

export const btn =
  'rounded border px-3 py-1.5 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400'
export const btnDefault = `${btn} border-slate-700 bg-slate-800 text-slate-100 hover:bg-slate-700`
export const btnDanger = `${btn} border-red-800 bg-red-950 text-red-200 hover:bg-red-900`
export const btnPrimary = `${btn} border-sky-700 bg-sky-900 text-sky-100 hover:bg-sky-800`
