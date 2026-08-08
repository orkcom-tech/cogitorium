import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, auth, type Model, type Team, type User, type Workspace } from '../api'

export default function WorkspacesPage({ me }: { me: User }) {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    Promise.all([api.workspaces.list(), api.models.list(), auth.teams()])
      .then(([w, m, t]) => {
        setWorkspaces(w)
        setModels(m)
        setTeams(t)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  useEffect(reload, [reload])

  return (
    <div className="page">
      <h2>Workspaces</h2>
      {error && <p className="error">{error}</p>}
      <CreateForm models={models} onDone={reload} />
      {workspaces.length === 0 ? (
        <p className="hint">
          No workspaces yet. A workspace is a team of agents behind one orchestrator chat —{' '}
          {models.length === 0 ? 'add a model to the catalog first, then ' : ''}create one above.
        </p>
      ) : (
        <div className="ws-list">
          {workspaces.map((w) => {
            const mine = w.owner_id === me.id
            return (
              <div key={w.id} className="card">
                <div className="card-head">
                  <Link to={`/workspaces/${w.id}`}>
                    <strong>{w.name}</strong>
                  </Link>
                  <span className="muted">{w.description}</span>
                  {!mine && <span className="muted">shared with you</span>}
                  {w.team_id != null && (
                    <span className="muted">· team: {teams.find((t) => t.id === w.team_id)?.name ?? w.team_id}</span>
                  )}
                  <span className="spacer" />
                  {(mine || me.role === 'admin') && (
                    <select
                      value={w.team_id ?? ''}
                      onChange={(e) =>
                        api.workspaces
                          .setTeam(w.id, e.target.value ? Number(e.target.value) : null)
                          .then(reload)
                          .catch((err: Error) => setError(err.message))
                      }
                    >
                      <option value="">private</option>
                      {teams.map((t) => (
                        <option key={t.id} value={t.id}>
                          share with {t.name}
                        </option>
                      ))}
                    </select>
                  )}
                  <button
                    onClick={() => {
                      const name = prompt(`Name for your copy of "${w.name}"?`, `${w.name} copy`)
                      if (name)
                        api.workspaces
                          .clone(w.id, name)
                          .then(reload)
                          .catch((err: Error) => setError(err.message))
                    }}
                    title="Copy the agents and wiring into a workspace of your own — the history stays here"
                  >
                    clone
                  </button>
                  {(mine || me.role === 'admin') && (
                    <button
                      className="danger"
                      onClick={() => {
                        if (confirm(`Delete workspace "${w.name}" with its agents and history?`))
                          api.workspaces.remove(w.id).then(reload).catch((e: Error) => setError(e.message))
                      }}
                    >
                      delete
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function CreateForm({ models, onDone }: { models: Model[]; onDone: () => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [modelId, setModelId] = useState<number | ''>('')
  const [error, setError] = useState<string | null>(null)

  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        if (modelId === '') {
          setError('pick a model for the orchestrator')
          return
        }
        api.workspaces
          .create({ name, description, orchestrator_model_id: modelId })
          .then(() => {
            setName('')
            setDescription('')
            setError(null)
            onDone()
          })
          .catch((e: Error) => setError(e.message))
      }}
    >
      <input required placeholder="workspace name" value={name} onChange={(e) => setName(e.target.value)} />
      <input
        className="grow"
        placeholder="description (optional)"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
      />
      <select value={modelId} onChange={(e) => setModelId(e.target.value ? Number(e.target.value) : '')}>
        <option value="">orchestrator model…</option>
        {models.map((m) => (
          <option key={m.id} value={m.id}>
            {m.provider_name} / {m.label || m.model_name}
          </option>
        ))}
      </select>
      <button type="submit" disabled={models.length === 0}>
        create workspace
      </button>
      {error && <span className="error">{error}</span>}
    </form>
  )
}
