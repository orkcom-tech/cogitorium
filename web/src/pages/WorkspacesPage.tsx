import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { usePublishShell } from '../shell'
import { STAGE_ICON } from '../shell-icons'
import { PALETTE, hueOf, isChosen } from './hue'
import { Select } from './Select'
import {
  api,
  auth,
  type ImportReport,
  type Model,
  type Team,
  type User,
  type Workspace,
  type WorkspaceBundle,
} from '../api'

export default function WorkspacesPage({ me }: { me: User }) {
  // The map belongs HERE, beside the list of everything, because that is what
  // it is for: seeing the whole install at once. Inside a single workspace it
  // was one stage among five, answering a question that screen does not ask.
  const nav = useNavigate()
  usePublishShell(
    () => ({
      here: { label: 'Workspaces' },
      stages: {
        items: [
          { id: 'list', title: 'Workspaces', icon: STAGE_ICON.workspaces },
          { id: 'map', title: 'Map', icon: STAGE_ICON.map },
        ],
        current: 'list',
        go: (id: string) => {
          if (id === 'map') nav('/map')
        },
      },
    }),
    [],
  )

  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [creating, setCreating] = useState(false)
  const [importing, setImporting] = useState(false)
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
        {/* Import is not disabled with an empty catalog, unlike creating one.
            A bundle names its models by provider and model name and resolves
            them here, so importing without them is a real thing to do: the
            workspace arrives whole and the report says which agents came in
            with nothing bound. */}
        <button onClick={() => setImporting(true)} title="Build a workspace from a bundle someone exported">
          import
        </button>
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

      {importing && <ImportDialog onClose={() => setImporting(false)} onImported={reload} />}

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

  const hue = hueOf(w)

  return (
    // The colour is carried as a variable rather than baked into each rule, so
    // the card, its spine and the picker below all read the one value.
    <div className="card ws-card" style={{ '--ws-hue': hue } as React.CSSProperties}>
      {/* The spine IS the picker.
          Ten saturated dots on every card meant thirty of them on a page whose
          whole rule is that green means running and red means broken. The
          colour is shown by the edge it colours, and choosing one is a click
          on that edge — which is also where you would point if asked to change
          it. */}
      <HuePicker w={w} onChanged={onChanged} onError={onError} />
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
          <Select
            value=""
            aria-label="Share with a team"
            placeholder="add a team…"
            onChange={(v) => {
              if (!v) return
              api.workspaces
                .share(w.id, Number(v))
                .then(onChanged)
                .catch((err: Error) => onError(err.message))
            }}
            options={rest.map((t) => ({ value: String(t.id), label: t.name }))}
          />
        )}
      </div>
    </div>
  )
}

// ImportDialog builds a workspace out of a bundle file.
//
// The report at the end is why this is a dialog and not a file button. An
// import can succeed and still differ from what the operator handed it: a
// model this install does not have leaves an agent with nothing bound, and a
// gear whose name is already in the catalog is left alone rather than
// overwritten. Neither one fails the import, so both are put in front of the
// operator here — the alternative is finding out when an agent fails a turn
// next week.
function ImportDialog({ onClose, onImported }: { onClose: () => void; onImported: () => void }) {
  const [bundle, setBundle] = useState<WorkspaceBundle | null>(null)
  const [name, setName] = useState('')
  const [gears, setGears] = useState(false)
  const [withContext, setWithContext] = useState(false)
  const [report, setReport] = useState<ImportReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // The file is read here rather than posted as it arrived, so the dialog can
  // say what is in it before anything is created — including which of the two
  // optional parts it carries at all. What a bundle MEANS is still the
  // server's judgement; this only answers "can I read this file".
  const read = (f: File) => {
    setError(null)
    f.text()
      .then((text) => {
        const parsed: unknown = JSON.parse(text)
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          throw new Error('it holds JSON, but not an object')
        }
        const b = parsed as WorkspaceBundle
        setBundle(b)
        setName(b.workspace?.name ?? '')
        setGears((b.gears?.length ?? 0) > 0)
        setWithContext((b.context?.length ?? 0) > 0)
      })
      .catch((e: Error) => {
        setBundle(null)
        setError(
          `${f.name} is not a bundle this can read: ${e.message}. Bundles come from the "export" button on a workspace.`,
        )
      })
  }

  const counts = {
    agents: bundle?.agents?.length ?? 0,
    wires: bundle?.wires?.length ?? 0,
    gears: bundle?.gears?.length ?? 0,
    context: bundle?.context?.length ?? 0,
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal create-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="row theme-head">
          <h3>{report ? 'Imported' : 'Import a workspace'}</h3>
          <span className="spacer" />
          <button onClick={onClose} title="Close">
            ×
          </button>
        </div>

        {report ? (
          <div className="create-body">
            <p>
              <Link to={`/workspaces/${report.workspace.id}`}>{report.workspace.name}</Link> —{' '}
              {report.agents} {report.agents === 1 ? 'agent' : 'agents'}, {report.wires}{' '}
              {report.wires === 1 ? 'wire' : 'wires'}
              {report.context_files > 0 &&
                `, ${report.context_files} context ${report.context_files === 1 ? 'document' : 'documents'}`}
              . It belongs to you.
            </p>

            {report.unresolved_models.length > 0 && (
              <>
                <p className="warn">
                  {report.unresolved_models.length}{' '}
                  {report.unresolved_models.length === 1 ? 'agent has' : 'agents have'} no model: this install
                  does not have the one the bundle named. They were created anyway, and cannot run until you
                  bind one.
                </p>
                {report.unresolved_models.map((u) => (
                  <div key={`${u.agent}/${u.model_name}`} className="row binding">
                    <code>{u.agent}</code>
                    <span className="muted">
                      wanted {u.provider_type} / {u.model_name}
                    </span>
                  </div>
                ))}
                <p className="hint">
                  Add it under <Link to="/models">Models</Link>, then pick it in the agent's inspector.
                </p>
              </>
            )}

            {report.gears_skipped.length > 0 && (
              <>
                <p className="warn">
                  {report.gears_skipped.length} {report.gears_skipped.length === 1 ? 'gear was' : 'gears were'}{' '}
                  skipped. The workspace expects them, so check that what you already have does the same thing.
                </p>
                {report.gears_skipped.map((g) => (
                  <div key={g.name} className="row binding">
                    <code>{g.name}</code>
                    <span className="muted">{g.why}</span>
                  </div>
                ))}
              </>
            )}

            {report.gears_imported.length > 0 && (
              <>
                <p>
                  {report.gears_imported.length} {report.gears_imported.length === 1 ? 'gear' : 'gears'} forged
                  here and left pending: somebody else's code never arrives approved.
                </p>
                <div className="row">
                  {report.gears_imported.map((g) => (
                    <code key={g}>{g}</code>
                  ))}
                </div>
                <p className="hint">
                  Read them under <Link to="/gears">Gears</Link> and approve what you want to run.
                </p>
              </>
            )}

            <div className="row">
              <span className="spacer" />
              <Link to={`/workspaces/${report.workspace.id}`} className="linkish">
                open it
              </Link>
              <button className="primary" onClick={onClose}>
                done
              </button>
            </div>
          </div>
        ) : (
          <div className="create-body">
            <label className="field">
              <span className="muted">the bundle file</span>
              <input
                type="file"
                accept="application/json,.json"
                onChange={(e) => {
                  const f = e.target.files?.[0]
                  if (f) read(f)
                }}
              />
            </label>

            {bundle && (
              <>
                <p className="hint">
                  {counts.agents} {counts.agents === 1 ? 'agent' : 'agents'}, {counts.wires}{' '}
                  {counts.wires === 1 ? 'wire' : 'wires'}, {counts.gears}{' '}
                  {counts.gears === 1 ? 'gear' : 'gears'}, {counts.context} context{' '}
                  {counts.context === 1 ? 'document' : 'documents'}.
                </p>
                <label className="field">
                  name
                  <input value={name} onChange={(e) => setName(e.target.value)} placeholder="research" />
                  <span className="hint">
                    Names are unique here. Change it to import a bundle next to the workspace it came from.
                  </span>
                </label>
                <label className="row">
                  <input
                    type="checkbox"
                    checked={gears}
                    disabled={counts.gears === 0}
                    onChange={(e) => setGears(e.target.checked)}
                  />
                  forge its gears
                </label>
                <span className="hint">
                  They arrive pending, whatever the bundle says — approval does not travel. A name you already
                  use is left untouched and reported.
                </span>
                <label className="row">
                  <input
                    type="checkbox"
                    checked={withContext}
                    disabled={counts.context === 0}
                    onChange={(e) => setWithContext(e.target.checked)}
                  />
                  write its context documents
                </label>
                <span className="hint">
                  They land under the new workspace's own branches, never where the bundle says. Contextverse
                  has to be reachable.
                </span>
              </>
            )}

            {error && <p className="error">{error}</p>}
            <div className="row">
              <span className="spacer" />
              <button type="button" onClick={onClose}>
                cancel
              </button>
              <button
                className="primary"
                disabled={busy || !bundle || !name.trim()}
                onClick={() => {
                  if (!bundle) return
                  setBusy(true)
                  setError(null)
                  api.workspaces
                    .importBundle({
                      name: name.trim(),
                      bundle,
                      include_gears: gears,
                      include_context: withContext,
                    })
                    .then((r) => {
                      setReport(r)
                      // The list behind the dialog is refreshed now, while the
                      // report stays up: closing it must not be what makes the
                      // new workspace appear.
                      onImported()
                    })
                    .catch((e: Error) => setError(e.message))
                    .finally(() => setBusy(false))
                }}
              >
                {busy ? 'importing…' : 'import'}
              </button>
            </div>
          </div>
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
            <Select
              value={modelId === '' ? '' : String(modelId)}
              aria-label="orchestrator model"
              onChange={(v) => setModelId(v ? Number(v) : '')}
              options={models.map((m) => ({
                value: String(m.id),
                label: `${m.provider_name} / ${m.label || m.model_name}`,
              }))}
            />
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

/**
 * The colour, chosen from the edge it colours.
 *
 * It is shared state rather than a personal preference — the point of "the
 * amber one" is that a team says it to each other — so anyone who can reach
 * the workspace may set it. Nothing here grants access, so there is nothing to
 * escalate by allowing it.
 */
function HuePicker({
  w,
  onChanged,
  onError,
}: {
  w: Workspace
  onChanged: () => void
  onError: (m: string) => void
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    // Deferred, or the click that opened it closes it on the same tick.
    const t = setTimeout(() => document.addEventListener('mousedown', away), 0)
    document.addEventListener('keydown', key)
    return () => {
      clearTimeout(t)
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', key)
    }
  }, [open])

  const set = (hue: number | null) =>
    api.workspaces
      .colour(w.id, hue)
      .then(() => {
        setOpen(false)
        onChanged()
      })
      .catch((e: Error) => onError(e.message))

  return (
    <div className="ws-hue" ref={box}>
      <button
        className="ws-spine"
        // The spine paints itself the workspace's colour, so it says so. Without
        // this the blanket button rule in tokens.css wins on weight and fills it
        // with the ordinary five-percent grey — the colour simply disappears.
        data-own
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        title={isChosen(w) ? 'change this workspace colour' : 'give this workspace a colour'}
      >
        <span className="sr-only">Colour</span>
      </button>
      {open && (
        <div className="hue-pop">
          {PALETTE.map((h) => (
            <button
              key={h}
              className={`hue-dot ${w.hue === h ? 'on' : ''}`}
              style={{ background: `hsl(${h} 68% 52%)` }}
              title={w.hue === h ? 'this colour' : 'give it this colour'}
              onClick={() => set(h)}
            />
          ))}
          {/* Clearing is not choosing grey: it hands the workspace back the
              colour derived from its id and records that nobody picked. */}
          <button className="hue-clear" disabled={!isChosen(w)} onClick={() => set(null)}>
            {isChosen(w) ? 'clear' : 'unset'}
          </button>
        </div>
      )}
    </div>
  )
}
