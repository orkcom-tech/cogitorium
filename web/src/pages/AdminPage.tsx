import { useCallback, useEffect, useState } from 'react'
import { api, auth, type GraphData, type Team, type User } from '../api'
import GraphCanvas from './GraphCanvas'
import { Select } from './Select'

// Users and teams. A single-operator install never needs this page — the
// seeded admin is the only account — so it exists for the moment an install
// stops being single-operator.
export default function AdminPage() {
  const [users, setUsers] = useState<User[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [issued, setIssued] = useState<{ name: string; token: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [map, setMap] = useState<GraphData | null>(null)
  const [mapLayers, setMapLayers] = useState<Record<string, boolean>>({})

  const reload = useCallback(() => {
    Promise.all([auth.users(), auth.teams(), api.graph.map()])
      .then(([u, t, m]) => {
        setUsers(u)
        setTeams(t)
        setMap(m)
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
        <h3>Access map</h3>
        <p className="hint">
          Who owns what and who can reach it — the same relationships the permission checks use, drawn instead of
          pieced together from the tables below. Click a colour in the legend to hide that layer.
        </p>
        <div className="map-canvas">
          <GraphCanvas
            data={map}
            layers={mapLayers}
            onToggleLayer={(kind) => setMapLayers((p) => ({ ...p, [kind]: p[kind] === false }))}
            emptyHint="Nothing to draw yet — create a workspace or a second user."
          />
        </div>
      </section>

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
      <Select
        value={role}
        aria-label="Role"
        onChange={setRole}
        options={[
          { value: 'member', label: 'member' },
          { value: 'team-lead', label: 'team-lead' },
          { value: 'admin', label: 'admin' },
        ]}
      />
      <input
        type="password"
        aria-label="Password"
        placeholder="optional"
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
    <Select
      value=""
      aria-label="Add to team"
      placeholder="add to team…"
      options={teams.map((t) => ({ value: String(t.id), label: t.name }))}
      onChange={(v) => {
        if (v) onPick(Number(v))
      }}
    />
  )
}
