import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, auth, type Model, type Team, type User, type Workspace } from '../api'

export default function WorkspacesPage({ me }: { me: User }) {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [creating, setCreating] = useState(false)
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

  const noModels = models.length === 0

  return (
    <div className="page">
      <div className="row page-head">
        <h2>Workspaces</h2>
        <span className="spacer" />
        {/* One obvious way in. The form used to be four controls in a row above
            the list, which read as part of the page furniture rather than as
            the thing you came here to do. */}
        <button className="primary big" onClick={() => setCreating(true)} disabled={noModels}>
          + New workspace
        </button>
      </div>
      {error && <p className="error">{error}</p>}

      {noModels && (
        <p className="hint">
          A workspace needs a model for its orchestrator, and the catalog is empty. Add one under{' '}
          <Link to="/models">Models</Link> first.
        </p>
      )}

      {creating && (
        <CreateDialog
          models={models}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false)
            reload()
          }}
        />
      )}

      {workspaces.length === 0 && !noModels ? (
        <div className="empty-state">
          <h3>Nothing here yet</h3>
          <p className="hint">
            A workspace is a group of agents behind one orchestrator chat. You talk to the orchestrator; it
            creates the others, wires them together and hands them work.
          </p>
          <button className="primary big" onClick={() => setCreating(true)}>
            + New workspace
          </button>
        </div>
      ) : (
        <div className="ws-list">
          {workspaces.map((w) => (
            <WorkspaceCard
              key={w.id}
              w={w}
              me={me}
              teams={teams}
              onChanged={reload}
              onError={setError}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function WorkspaceCard({
  w,
  me,
  teams,
  onChanged,
  onError,
}: {
  w: Workspace
  me: User
  teams: Team[]
  onChanged: () => void
  onError: (m: string) => void
}) {
  const mine = w.owner_id === me.id
  const mayShare = me.role === 'admin'
  const shared = teams.filter((t) => w.team_ids?.includes(t.id))
  const rest = teams.filter((t) => !w.team_ids?.includes(t.id))

  return (
    <div className="card ws-card">
      <div className="card-head">
        <Link to={`/workspaces/${w.id}`} className="ws-title">
          <strong>{w.name}</strong>
        </Link>
        <span className="muted">{w.description}</span>
        {!mine && <span className="badge">shared with you</span>}
        <span className="spacer" />
        <button
          onClick={() => {
            const name = prompt(`Name for your copy of "${w.name}"?`, `${w.name} copy`)
            if (name) api.workspaces.clone(w.id, name).then(onChanged).catch((e: Error) => onError(e.message))
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
                api.workspaces.remove(w.id).then(onChanged).catch((e: Error) => onError(e.message))
            }}
          >
            delete
          </button>
        )}
      </div>

      {/* Sharing is a list, not a picker. A workspace can go to any number of
          teams, and each one is withdrawn on its own rather than by replacing
          whoever currently has it. */}
      <div className="row share-row">
        <span className="muted">shared with</span>
        {shared.length === 0 && <span className="muted">nobody — only you and admins</span>}
        {shared.map((t) => (
          <span key={t.id} className="team-chip">
            {t.name}
            {mayShare && (
              <button
                title={`Stop sharing with ${t.name}`}
                onClick={() =>
                  api.workspaces.unshare(w.id, t.id).then(onChanged).catch((e: Error) => onError(e.message))
                }
              >
                ×
              </button>
            )}
          </span>
        ))}
        {mayShare && rest.length > 0 && (
          <select
            value=""
            onChange={(e) => {
              if (!e.target.value) return
              api.workspaces
                .share(w.id, Number(e.target.value))
                .then(onChanged)
                .catch((err: Error) => onError(err.message))
            }}
          >
            <option value="">add a team…</option>
            {rest.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        )}
      </div>
    </div>
  )
}

function CreateDialog({
  models,
  onClose,
  onCreated,
}: {
  models: Model[]
  onClose: () => void
  onCreated: () => void
}) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [modelId, setModelId] = useState<number | ''>(models[0]?.id ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal create-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="row theme-head">
          <h3>New workspace</h3>
          <span className="spacer" />
          <button onClick={onClose} title="Close">
            ×
          </button>
        </div>
        <form
          className="create-body"
          onSubmit={(e) => {
            e.preventDefault()
            if (modelId === '') {
              setError('pick a model for the orchestrator')
              return
            }
            setBusy(true)
            api.workspaces
              .create({ name: name.trim(), description: description.trim(), orchestrator_model_id: modelId })
              .then(onCreated)
              .catch((err: Error) => setError(err.message))
              .finally(() => setBusy(false))
          }}
        >
          <label className="field">
            name
            <input autoFocus required value={name} onChange={(e) => setName(e.target.value)} placeholder="research" />
          </label>
          <label className="field">
            what it is for <span className="muted">(optional)</span>
            <input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="drafting and checking the release notes"
            />
          </label>
          <label className="field">
            orchestrator model
            <select value={modelId} onChange={(e) => setModelId(e.target.value ? Number(e.target.value) : '')}>
              {models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.provider_name} / {m.label || m.model_name}
                </option>
              ))}
            </select>
            <span className="hint">
              This is the agent you will talk to. It creates the others and hands them work, so give it a model
              you trust to reason — the workers can be cheaper ones.
            </span>
          </label>
          {error && <p className="error">{error}</p>}
          <div className="row">
            <span className="spacer" />
            <button type="button" onClick={onClose}>
              cancel
            </button>
            <button className="primary" type="submit" disabled={busy || !name.trim()}>
              {busy ? 'creating…' : 'create workspace'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
