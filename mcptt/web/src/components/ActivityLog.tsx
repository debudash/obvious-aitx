// Activity view — everything this console observed, newest first. This is the
// dispatcher's local audit trail; the server's own audit log is a separate
// (route-gated) surface.
import type { ConsoleState } from '../state/consoleStore'
import { clockTime } from '../lib/format'
import { EmptyState, Panel } from './ui'

export function ActivityLog({ state }: { state: ConsoleState }) {
  return (
    <Panel title="Activity">
      {state.eventLog.length === 0 ? (
        <EmptyState>Nothing observed yet — connect and act to fill this log.</EmptyState>
      ) : (
        <ol className="max-h-64 space-y-0.5 overflow-y-auto font-mono text-xs" aria-label="Activity log">
          {state.eventLog.map((e) => (
            <li key={e.id} className="flex gap-3 rounded px-2 py-1 hover:bg-slate-800/60">
              <span className="shrink-0 text-slate-500">{clockTime(e.at)}</span>
              <span className="shrink-0 text-sky-400">{e.kind}</span>
              <span className="min-w-0 text-slate-300">{e.text}</span>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  )
}
