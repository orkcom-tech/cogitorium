import { useState } from 'react'
import { auth } from '../api'
import { session } from '../session'

// The one door into the app, shared by every shell. Served from a server the
// address is already known, so it stays out of the way; a desktop shell has
// no origin of its own and needs it, so it is offered — with the servers
// seen before, to save retyping.
export default function LoginPage({ onSignedIn }: { onSignedIn: () => void }) {
  const servedFromServer = !!window.location.origin && window.location.protocol.startsWith('http')
  const [server, setServer] = useState(session.server())
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const known = session.knownServers()

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    session.setServer(server)
    auth
      .login(name, password)
      .then((r) => {
        session.setToken(r.token)
        onSignedIn()
      })
      .catch((err: Error) => setError(err.message))
      .finally(() => setBusy(false))
  }

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <h1 className="brand">Cogitorium</h1>
        <label className="field">
          <span className="muted">server</span>
          <input
            placeholder={servedFromServer ? window.location.origin : 'https://cogitorium.example.com'}
            value={server}
            onChange={(e) => setServer(e.target.value)}
            list="known-servers"
          />
          <datalist id="known-servers">
            {known.map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
        </label>
        <label className="field">
          <span className="muted">user</span>
          <input required autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="field">
          <span className="muted">password</span>
          <input required type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {error && <p className="error">{error}</p>}
        <button type="submit" disabled={busy || !name || !password}>
          {busy ? 'signing in…' : 'sign in'}
        </button>
        <p className="hint">
          On your own machine Cogitorium signs you in as the admin automatically — this page is for reaching a
          server over the network.
        </p>
      </form>
    </div>
  )
}
