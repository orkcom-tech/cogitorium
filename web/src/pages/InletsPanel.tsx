import { useCallback, useEffect, useState } from 'react'
import {
  api,
  inletDeliveryUrl,
  type Agent,
  type Inlet,
  type InletRun,
  type InletRunState,
  type InletTask,
  type IssuedInletKey,
} from '../api'

// The doors into this workspace from outside.
//
// This is a panel on the workspace rather than a page of its own because every
// question it raises is answered next to it: a delivery that failed at 3am is
// almost always the agent behind the task, the instruction it was given, or the
// schema the caller missed — and the roster, the blueprint and this list are
// then one bench away from each other instead of one navigation.

// What each ledger state means, in the operator's terms rather than the
// engine's. A state name alone tells you a run went wrong; these tell you whose
// problem it is, which is the only thing worth knowing at 3am.
const STATE_MEANING: Record<InletRunState, string> = {
  accepted: 'The key opened the door and the task existed; the payload had not been looked at yet.',
  refused_schema:
    'The payload did not match what this task accepts. No model was called, so nothing was spent — the caller sent the wrong thing.',
  running: 'The agent was working when this row was last written.',
  completed: 'The agent answered, and the answer went back to the caller on the same response.',
  failed:
    'Something went wrong after the payload was accepted. This one is about the workspace — a missing agent, no model bound, a run already in progress — not about what the caller sent.',
  interrupted:
    'The caller hung up, or this server stopped while the run was live. Whether the agent finished its work is not known.',
}

export default function InletsPanel({
  wsId,
  agents,
  shown,
  onError,
}: {
  wsId: number
  agents: Agent[]
  /** whether the panel is placed on the bench — see the load effect below */
  shown: boolean
  onError: (m: string) => void
}) {
  const [inlets, setInlets] = useState<Inlet[]>([])
  const [runs, setRuns] = useState<InletRun[]>([])
  // The one copy of a key that will ever exist. Held here rather than in the
  // card that produced it so that deleting or reloading around it cannot take
  // the operator's only chance to copy it off the screen.
  const [issued, setIssued] = useState<(IssuedInletKey & { address: string }) | null>(null)
  const [looked, setLooked] = useState<InletRun | null>(null)

  const reload = useCallback(
    () =>
      Promise.all([api.inlets.list(wsId), api.inlets.runs(wsId)])
        .then(([i, r]) => {
          setInlets(i)
          setRuns(r)
        })
        .catch((e: Error) => onError(e.message)),
    [wsId, onError],
  )

  // Every panel on the bench stays mounted whether or not it is on screen, so
  // an unconditional load here would put two queries on every workspace open
  // for a feature most workspaces never use. The fetch waits until the operator
  // has actually put the panel somewhere.
  useEffect(() => {
    if (shown) void reload()
  }, [shown, reload])

  return (
    <div className="inlets">
      <p className="hint">
        A door into this workspace from outside. A caller posts to its address with its key, one task runs on one
        agent, and the answer comes back on the same response. Nothing is added to the conversation, and nothing is
        remembered between deliveries — the payload is treated as text from a stranger, so a run through a door cannot
        write to the instruction library, forge a gear or change the blueprint. A leaked key is closed by issuing a new
        one, which retires it.
      </p>

      {issued && (
        <div className="card">
          <p className="notice">
            Key for <strong>{issued.address}</strong> — {issued.notice}
          </p>
          <pre className="prompt-preview">{issued.key}</pre>
          <div className="row">
            <button onClick={() => setIssued(null)}>done</button>
          </div>
        </div>
      )}

      <NewInletForm
        wsId={wsId}
        onCreated={(r) => {
          setIssued({ address: r.inlet.address, key: r.key, notice: r.notice })
          void reload()
        }}
        onError={onError}
      />

      {inlets.length === 0 ? (
        <p className="hint">No doors on this workspace. Everything reaches it through you.</p>
      ) : (
        inlets.map((i) => (
          <InletCard
            key={i.id}
            inlet={i}
            agents={agents}
            onIssued={(r) => {
              setIssued({ address: i.address, key: r.key, notice: r.notice })
              void reload()
            }}
            onChanged={reload}
            onError={onError}
          />
        ))
      )}

      <h3>Deliveries</h3>
      <p className="hint">
        Every delivery is recorded before the work starts, so a run that never came back still left a row. The number
        here is the one the caller was answered with. The fifty most recent are listed; anything older is still on
        record and comes back by its number.
      </p>
      <div className="row">
        <LookupRunForm onFound={setLooked} onError={onError} />
        <span className="spacer" />
        <button onClick={() => void reload()}>refresh</button>
      </div>
      {looked && (
        <>
          <div className="row">
            <span className="muted">looked up:</span>
            <span className="spacer" />
            <button onClick={() => setLooked(null)}>clear</button>
          </div>
          <RunRow run={looked} />
        </>
      )}
      {runs.length === 0 ? (
        <p className="hint">Nothing has come through a door in this workspace.</p>
      ) : (
        runs.map((r) => <RunRow key={r.id} run={r} />)
      )}
    </div>
  )
}

function NewInletForm({
  wsId,
  onCreated,
  onError,
}: {
  wsId: number
  onCreated: (r: IssuedInletKey & { inlet: Inlet }) => void
  onError: (m: string) => void
}) {
  const [address, setAddress] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)

  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        setBusy(true)
        api.inlets
          .create(wsId, address.trim(), description.trim())
          .then((r) => {
            setAddress('')
            setDescription('')
            onCreated(r)
          })
          .catch((err: Error) => onError(err.message))
          .finally(() => setBusy(false))
      }}
    >
      <input
        required
        placeholder="address, e.g. tickets"
        value={address}
        onChange={(e) => setAddress(e.target.value)}
      />
      <input
        className="grow"
        placeholder="what comes through this door"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
      />
      <button type="submit" disabled={busy || !address.trim()}>
        open a door
      </button>
    </form>
  )
}

function InletCard({
  inlet,
  agents,
  onIssued,
  onChanged,
  onError,
}: {
  inlet: Inlet
  agents: Agent[]
  onIssued: (r: IssuedInletKey) => void
  onChanged: () => void
  onError: (m: string) => void
}) {
  const [adding, setAdding] = useState(false)

  return (
    <div className="card">
      <div className="card-head">
        <code>{inlet.address}</code>
        {inlet.description && <span className="muted">{inlet.description}</span>}
        <span className="spacer" />
        <button
          onClick={() => {
            if (
              !confirm(
                `Issue a new key for "${inlet.address}"? The current one stops working immediately, and anything using it starts getting 401.`,
              )
            )
              return
            api.inlets.issueKey(inlet.id).then(onIssued).catch((e: Error) => onError(e.message))
          }}
          title="Issue a fresh key and retire the current one"
        >
          new key
        </button>
        <button
          className="danger"
          onClick={() => {
            if (
              !confirm(
                `Delete the inlet "${inlet.address}"? Its tasks go with it and its key stops working. The record of what already came through it stays.`,
              )
            )
              return
            api.inlets.remove(inlet.id).then(onChanged).catch((e: Error) => onError(e.message))
          }}
        >
          delete
        </button>
      </div>

      {inlet.has_key ? (
        <p className="muted">
          key issued {inlet.key_issued_at} · {inlet.key_last_used_at ? `last used ${inlet.key_last_used_at}` : 'never used'}
        </p>
      ) : (
        <p className="notice">
          This door has no key, so every delivery to it is refused. Issue one to open it.
        </p>
      )}

      {inlet.tasks.length === 0 ? (
        <p className="hint">
          No tasks behind this door, so every address under it answers 404. A key on its own opens nothing.
        </p>
      ) : (
        inlet.tasks.map((t) => (
          <TaskRow
            key={t.id}
            inlet={inlet}
            task={t}
            onDeleted={onChanged}
            onError={onError}
          />
        ))
      )}

      {adding ? (
        <AddTaskForm
          inlet={inlet}
          agents={agents}
          onDone={() => {
            setAdding(false)
            onChanged()
          }}
          onCancel={() => setAdding(false)}
          onError={onError}
        />
      ) : (
        <div className="row">
          <button onClick={() => setAdding(true)}>add a task</button>
        </div>
      )}
    </div>
  )
}

function TaskRow({
  inlet,
  task,
  onDeleted,
  onError,
}: {
  inlet: Inlet
  task: InletTask
  onDeleted: () => void
  onError: (m: string) => void
}) {
  const url = inletDeliveryUrl(inlet.address, task.name)
  return (
    <div className="inlet-task">
      <div className="row">
        <code className="grow inlet-url">POST {url}</code>
        <button
          className="danger"
          onClick={() => {
            if (!confirm(`Delete the task "${task.name}"? Calls to it start answering 404.`)) return
            api.inlets.removeTask(task.id).then(onDeleted).catch((e: Error) => onError(e.message))
          }}
        >
          delete
        </button>
      </div>
      <p className="muted">
        {task.accepts === 'file'
          ? `a file, ${task.content_type || 'of any type'}`
          : 'a JSON payload'}{' '}
        → {task.agent_name}
      </p>
      <p className="hint">{task.instruction}</p>
      <details>
        <summary>what a caller sends</summary>
        {task.accepts === 'file' ? (
          <>
            <pre className="prompt-preview">
              {`curl -sS -X POST "${url}" \\
  -H "Authorization: Bearer $INLET_KEY" \\
  -H "Content-Type: ${task.content_type.includes('*') || !task.content_type ? '<the file’s media type>' : task.content_type}" \\
  --data-binary @<your file>`}
            </pre>
            <p className="hint">
              The file is written into this workspace under <code>inlets/{inlet.address}/</code> and the agent is given
              its path, never its bytes — that is what lets a gear take it from there. A multipart form works too.
            </p>
          </>
        ) : (
          <>
            <pre className="prompt-preview">
              {`curl -sS -X POST "${url}" \\
  -H "Authorization: Bearer $INLET_KEY" \\
  -H "Content-Type: application/json" \\
  --data @payload.json`}
            </pre>
            <p className="hint">The body is checked against this schema before any model is called:</p>
            <pre className="prompt-preview">{prettySchema(task.schema)}</pre>
          </>
        )}
      </details>
    </div>
  )
}

function AddTaskForm({
  inlet,
  agents,
  onDone,
  onCancel,
  onError,
}: {
  inlet: Inlet
  agents: Agent[]
  onDone: () => void
  onCancel: () => void
  onError: (m: string) => void
}) {
  const [name, setName] = useState('')
  const [accepts, setAccepts] = useState<'json' | 'file'>('json')
  const [schemaText, setSchemaText] = useState('')
  const [contentType, setContentType] = useState('')
  const [agent, setAgent] = useState('')
  const [instruction, setInstruction] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    let schema: unknown
    if (accepts === 'json' && schemaText.trim()) {
      try {
        schema = JSON.parse(schemaText)
      } catch (err) {
        // Caught here rather than sent, because the server would report a
        // decoding failure on the whole request body and the operator would go
        // looking for the mistake in the wrong field.
        onError(`this schema is not valid JSON: ${err instanceof Error ? err.message : String(err)}`)
        return
      }
    }
    setBusy(true)
    api.inlets
      .addTask(inlet.id, {
        name: name.trim(),
        accepts,
        schema,
        content_type: accepts === 'file' ? contentType.trim() : undefined,
        agent,
        instruction: instruction.trim(),
      })
      .then(onDone)
      .catch((err: Error) => onError(err.message))
      .finally(() => setBusy(false))
  }

  return (
    <form className="card inlet-task-form" onSubmit={submit}>
      <div className="row">
        <input required placeholder="task name, e.g. classify" value={name} onChange={(e) => setName(e.target.value)} />
        <select value={accepts} onChange={(e) => setAccepts(e.target.value as 'json' | 'file')}>
          <option value="json">accepts JSON</option>
          <option value="file">accepts a file</option>
        </select>
        <select className="grow" required value={agent} onChange={(e) => setAgent(e.target.value)}>
          <option value="">which agent does the work…</option>
          {agents.map((a) => (
            <option key={a.id} value={a.name}>
              {a.name}
              {a.model_id === null ? ' — no model bound' : ''}
            </option>
          ))}
        </select>
      </div>

      {accepts === 'json' ? (
        <label className="field">
          <span className="muted">schema — leave it empty to accept any JSON body</span>
          <textarea
            rows={6}
            value={schemaText}
            onChange={(e) => setSchemaText(e.target.value)}
            placeholder={'{\n  "type": "object",\n  "required": ["subject"],\n  "properties": {\n    "subject": { "type": "string", "maxLength": 200 }\n  }\n}'}
          />
          <span className="hint">
            Only the keywords this server can actually enforce are accepted: type, enum, properties, required,
            additionalProperties, items, minItems, maxItems, minLength, maxLength, minimum, maximum. Anything else is
            refused here rather than stored and quietly not checked — a schema that promises validation it never
            performs is worse than none.
          </span>
        </label>
      ) : (
        <label className="field">
          <span className="muted">content type — leave it empty to accept any file</span>
          <input placeholder="image/* or image/png" value={contentType} onChange={(e) => setContentType(e.target.value)} />
        </label>
      )}

      <label className="field">
        <span className="muted">instruction</span>
        <textarea
          rows={3}
          required
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
          placeholder="recognise what is in this image and put it in the right bucket"
        />
        <span className="hint">
          The sentence the agent is given along with the payload. It is the whole brief — this run carries none of the
          conversation and nothing from the last delivery.
        </span>
      </label>

      <div className="row">
        <span className="spacer" />
        <button type="button" onClick={onCancel}>
          cancel
        </button>
        <button className="primary" type="submit" disabled={busy}>
          add task
        </button>
      </div>
    </form>
  )
}

function LookupRunForm({ onFound, onError }: { onFound: (r: InletRun) => void; onError: (m: string) => void }) {
  const [id, setId] = useState('')
  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        const n = Number(id)
        if (!n) return
        api.inlets.run(n).then(onFound).catch((err: Error) => onError(err.message))
      }}
    >
      <input placeholder="run number" value={id} onChange={(e) => setId(e.target.value)} />
      <button type="submit" disabled={!Number(id)}>
        look up
      </button>
    </form>
  )
}

function RunRow({ run }: { run: InletRun }) {
  return (
    <details className="inlet-run">
      <summary>
        <span className={`status ${run.state}`}>{run.state.replace('_', ' ')}</span>
        <code>#{run.id}</code>
        <span>
          {run.inlet_address}/{run.task_name}
        </span>
        <span className="muted">{run.agent_name}</span>
        <span className="muted">{run.created_at}</span>
      </summary>
      <div className="inlet-run-body">
        <p className="hint">{STATE_MEANING[run.state]}</p>
        <p className="muted">
          {/* The size is recorded when the payload is accepted, so a run
              refused before that has no size rather than a size of zero.
              Printing "0 bytes delivered" for a body the caller certainly
              sent would send an operator looking for a caller that posts
              nothing. */}
          {run.payload_bytes > 0 && <>{size(run.payload_bytes)} delivered · </>}
          settled {run.updated_at}
          {run.payload_path && (
            <>
              {' '}
              · landed at <code>{run.payload_path}</code>
            </>
          )}
        </p>
        {run.error && <pre className="prompt-preview run-failed">{run.error}</pre>}
        {run.result && <pre className="prompt-preview">{run.result}</pre>}
      </div>
    </details>
  )
}

// prettySchema shows a stored schema the way it would be written by hand. A
// schema that will not parse is corruption of the row rather than anything the
// operator typed, so it is shown exactly as stored instead of being repaired.
function prettySchema(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

function size(n: number): string {
  if (n < 1024) return `${n} bytes`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / (1024 * 1024)).toFixed(1)} MiB`
}
