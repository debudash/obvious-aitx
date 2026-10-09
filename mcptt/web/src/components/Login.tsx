// Login screen — the only conventional form in the console. Enter the demo
// dispatcher credentials and the session hook takes over.
import { useState, type FormEvent } from 'react'
import { ApiClient } from '../api/client'
import { btnPrimary } from './ui'

export function Login(props: { onToken: (token: string) => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await ApiClient.login(username.trim(), password)
      props.onToken(res.token)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="flex min-h-dvh items-center justify-center bg-slate-950 px-4">
      <form
        onSubmit={submit}
        className="w-full max-w-sm rounded-lg border border-slate-800 bg-slate-900/70 p-6"
        aria-label="Dispatcher login"
      >
        <h1 className="mb-1 text-xl font-semibold text-slate-100">MCPTT Dispatch</h1>
        <p className="mb-6 text-sm text-slate-400">Sign in with your dispatcher account.</p>
        <label className="block text-sm text-slate-300" htmlFor="login-username">
          Username
          <input
            id="login-username"
            name="username"
            autoComplete="username"
            autoFocus
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="mt-1 w-full rounded border border-slate-700 bg-slate-950 px-3 py-2 text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400"
          />
        </label>
        <label className="mt-4 block text-sm text-slate-300" htmlFor="login-password">
          Password
          <input
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="mt-1 w-full rounded border border-slate-700 bg-slate-950 px-3 py-2 text-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400"
          />
        </label>
        {error && (
          <p role="alert" className="mt-4 rounded border border-red-800 bg-red-950/60 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}
        <button type="submit" disabled={busy || !username || !password} className={`${btnPrimary} mt-6 w-full justify-center`}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}
