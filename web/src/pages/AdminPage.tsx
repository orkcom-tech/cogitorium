import { useCallback, useEffect, useState } from 'react'
import { auth, type Team, type User } from '../api'

// Users and teams. A single-operator install never needs this page — the
// seeded admin is the only account — so it exists for the moment an install
// stops being single-operator.
export default function AdminPage() {
  const [users, setUsers] = useState<User[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [issued, setIssued] = useState<{ name: string; token: string } | null>(null)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    Promise.all([auth.users(), auth.teams()])
      .then(([u, t]) => {
        setUsers(u)
        setTeams(t)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  useEffect(reload, [reload])

  return (
    <div className="page">
      <h2>People</h2>
      {error && <p className="error">{error}</p>}
      {issued && (
        <div className="card">
          <p className="notice">
            Token for <strong>{issued.name}</strong> — shown once, only its hash is stored. Copy it now.
          </p>
          <pre className="prompt-preview">{issued.token}</pre>
          <div className="row">
            <button onClick={() => setIssued(null)}>done</button>
          </div>
        </div>
      )}

      <section>
        <h3>Users</h3>
        <NewUserForm onCreated={(name, token) => { setIssued({ name, token }); reload() }} onError={setError} />
        <table>
          <thead>
            <tr>
              <th>user</th>
              <th>role</th>
              <th>teams</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>{u.name}</td>
                <td className="muted">{u.role}</td>
                <td className="muted">
                  {u.teams.map((id) => teams.find((t) => t.id === id)?.name ?? id).join(', ') || '—'}
                </td>
                <td>
                  <TeamPicker
                    teams={teams.filter((t) => !u.teams.includes(t.id))}
                    onPick={(teamId) =>
                      auth.addMember(teamId, u.id).then(reload).catch((e: Error) => setError(e.message))
                    }
                  />
                  {u.name !== 'admin' && (
                    <button
                      className="danger"
                      onClick={() => {
                        if (confirm(`Delete user "${u.name}"? Their tokens stop working immediately.`))
                          auth.deleteUser(u.id).then(reload).catch((e: Error) => setError(e.message))
                      }}
                    >
                      delete
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section>
        <h3>Teams</h3>
        <p className="hint">A workspace shared with a team is reachable by every member of it.</p>
        <NewTeamForm onDone={reload} onError={setError} />
        {teams.map((t) => (
          <div key={t.id} className="row binding">
            <code>{t.name}</code>
            <span className="muted">
              {users.filter((u) => u.teams.includes(t.id)).map((u) => u.name).join(', ') || 'no members'}
            </span>
            <span className="spacer" />
            <button
              className="danger"
              onClick={() => {
                if (confirm(`Delete team "${t.name}"? Workspaces shared with it stop being shared.`))
                  auth.deleteTeam(t.id).then(reload).catch((e: Error) => setError(e.message))
              }}
            >
              delete
            </button>
          </div>
        ))}
      </section>
    </div>
  )
}

function NewUserForm({
  onCreated,
  onError,
}: {
  onCreated: (name: string, token: string) => void
  onError: (m: string) => void
}) {
  const [name, setName] = useState('')
  const [role, setRole] = useState('member')
  const [password, setPassword] = useState('')

  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        auth
          .createUser({ name, role, password: password || undefined })
          .then((r) => {
            setName('')
            setPassword('')
            onCreated(r.user.name, r.token)
          })
          .catch((err: Error) => onError(err.message))
      }}
    >
      <input required placeholder="user name" value={name} onChange={(e) => setName(e.target.value)} />
      <select value={role} onChange={(e) => setRole(e.target.value)}>
        <option value="member">member</option>
        <option value="team-lead">team-lead</option>
        <option value="admin">admin</option>
      </select>
      <input
        type="password"
        placeholder="password (optional — token works without one)"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      <button type="submit">add user</button>
    </form>
  )
}

function NewTeamForm({ onDone, onError }: { onDone: () => void; onError: (m: string) => void }) {
  const [name, setName] = useState('')
  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        auth
          .createTeam(name)
          .then(() => {
            setName('')
            onDone()
          })
          .catch((err: Error) => onError(err.message))
      }}
    >
      <input required placeholder="team name" value={name} onChange={(e) => setName(e.target.value)} />
      <button type="submit">add team</button>
    </form>
  )
}

function TeamPicker({ teams, onPick }: { teams: Team[]; onPick: (id: number) => void }) {
  if (teams.length === 0) return null
  return (
    <select
      value=""
      onChange={(e) => {
        if (e.target.value) onPick(Number(e.target.value))
      }}
    >
      <option value="">add to team…</option>
      {teams.map((t) => (
        <option key={t.id} value={t.id}>
          {t.name}
        </option>
      ))}
    </select>
  )
}
