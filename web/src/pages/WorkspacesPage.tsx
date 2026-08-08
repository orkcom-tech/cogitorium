import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Model, type Workspace } from '../api'

export default function WorkspacesPage() {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    Promise.all([api.workspaces.list(), api.models.list()])
      .then(([w, m]) => {
        setWorkspaces(w)
        setModels(m)
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
          {workspaces.map((w) => (
            <div key={w.id} className="card">
              <div className="card-head">
                <Link to={`/workspaces/${w.id}`}>
                  <strong>{w.name}</strong>
                </Link>
                <span className="muted">{w.description}</span>
                <span className="spacer" />
                <button
                  className="danger"
                  onClick={() => {
                    if (confirm(`Delete workspace "${w.name}" with its agents and history?`))
                      api.workspaces.remove(w.id).then(reload).catch((e: Error) => setError(e.message))
                  }}
                >
                  delete
                </button>
              </div>
            </div>
          ))}
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
