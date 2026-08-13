import { useCallback, useEffect, useState } from 'react'
import {
  api,
  inletDeliveryUrl,
  type Agent,
  type Inlet,
  type InletExpect,
  type InletRun,
  type InletRunRecord,
  type InletRunState,
  type InletTask,
  type IssuedInletKey,
} from '../api'

// The receivers: the doors into this workspace from outside.
//
// Called receivers on screen and inlets in the wire, deliberately. "Inlet" is
// a word this product invented and nobody arrives already knowing; "receiver"
// says what the thing does. The URL, the API paths and the tables keep the old
// name because callers outside this install hold those strings, and breaking
// them to improve a label is the wrong trade.
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
  queued:
    'Waiting for this workspace to be free. Nothing is wrong: a workspace runs one thing at a time, and this one is in line. It used to be recorded as a failure, which is why the distinction is worth having.',
  refused_schema:
    'The payload did not match what this task accepts. No model was called, so nothing was spent — the caller sent the wrong thing.',
  running: 'The agent was working when this row was last written.',
  completed: 'The agent answered, and the answer went back to the caller on the same response.',
  failed:
    'Something went wrong after the payload was accepted. This one is about the workspace — a missing agent, no model bound, a run already in progress — not about what the caller sent.',
  interrupted:
    'The caller hung up, or this server stopped while the run was live. Whether the agent finished its work is not known.',
  refused_expectation:
    'The run did not do what this task requires — the gear it names never ran, or the files it needs never appeared. The agent may have answered beautifully; that is not what was checked. The record below is what was checked.',
  refused_output_schema:
    'The work happened and the answer came back the wrong shape. This one is about the model, not about the workspace: the run is in the record, and what it produced is in the message.',
  refused_budget:
    'This run reached the token ceiling set for one delivery and was stopped before its next model call. Not a fault: somebody drew this line. Retrying it unchanged will stop in the same place.',
}

// A schema an operator can start from and edit, rather than an example that
// disappears when they touch the field. Every keyword in it is one this server
// actually enforces.
const SCHEMA_STARTER = `{
  "type": "object",
  "required": ["url"],
  "additionalProperties": false,
  "properties": {
    "url": { "type": "string", "maxLength": 2000 },
    "depth": { "type": "integer", "minimum": 1, "maximum": 3 }
  }
}`

// schemaProblem is the JSON check only — whether the KEYWORDS are ones this
// server enforces is the server's answer, and asking the browser to duplicate
// that list is how the two drift apart.
function schemaProblem(text: string): string {
  try {
    const v = JSON.parse(text)
    if (typeof v !== 'object' || v === null || Array.isArray(v)) return 'a schema must be a JSON object'
    return ''
  } catch (e) {
    return `not JSON yet: ${(e as Error).message}`
  }
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
        A receiver: a door into this workspace from outside. A caller posts to its address with its key, one task runs on one
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
        <p className="hint">No receivers on this workspace. Everything reaches it through you.</p>
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
        record and comes back by its number. Open one to see what it did — which tools ran and how each ended, which
        files appeared — which is the part worth reading first, because it is the part no model wrote.
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
        <p className="hint">Nothing has come through a receiver in this workspace.</p>
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
        placeholder="what comes through this receiver"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
      />
      <button type="submit" disabled={busy || !address.trim()}>
        add a receiver
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
  // A receiver with no tasks is a receiver that answers 404 to everything —
  // this card says so two lines up. So the form is OPEN, not behind a button:
  // the one state in which there is exactly one thing to do should not make
  // anybody find the button for it.
  //
  // Once a task exists the button comes back, because by then adding a second
  // one is a choice rather than the only way forward.
  const [adding, setAdding] = useState(inlet.tasks.length === 0)

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
                `Delete the receiver "${inlet.address}"? Its tasks go with it and its key stops working. The record of what already came through it stays.`,
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
          This receiver has no key, so every delivery to it is refused. Issue one to open it.
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
      <TaskExpectations expect={task.expect} />
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
        {task.expect.schema != null && (
          <>
            <p className="hint">
              And what comes back has to match this, or the delivery answers 500 with{' '}
              <code>refused_output_schema</code> and the answer it refused:
            </p>
            <pre className="prompt-preview">{JSON.stringify(task.expect.schema, null, 2)}</pre>
          </>
        )}
      </details>
    </div>
  )
}

// What this task requires of a run, in the order an operator asks about it: did
// the work happen, then did the answer come back usable.
//
// A task that requires nothing shows nothing rather than a row of "no"s — the
// absence is the normal case, and a panel that repeated it for every task would
// make the tasks that DO require something harder to spot, not easier.
function TaskExpectations({ expect }: { expect: InletExpect }) {
  const requirements = expectationLines(expect)
  if (requirements.length === 0) return null
  return (
    <div className="inlet-expect">
      <p className="muted">this run only counts if:</p>
      <ul>
        {requirements.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
      <p className="hint">
        Checked against the record of what the run did, never against what the agent wrote — so a run that answers
        “done, I ran the gear” without having called it is refused, and the ledger says so.
      </p>
    </div>
  )
}

function expectationLines(expect: InletExpect): string[] {
  const lines: string[] = []
  if (expect.runs_gear) lines.push(`the gear ${expect.runs_gear} ran and exited successfully`)
  if (expect.produces_files)
    lines.push(
      `at least ${expect.produces_files} ${expect.produces_files === 1 ? 'file' : 'files'} appeared during the run`,
    )
  if (expect.answer_from === 'gear')
    lines.push('a gear ran — its output is the answer, and the agent’s own words are not returned at all')
  if (expect.schema != null) lines.push('the answer is JSON matching the shape below')
  return lines
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
  const [runsGear, setRunsGear] = useState('')
  const [producesFiles, setProducesFiles] = useState('')
  const [answerFrom, setAnswerFrom] = useState<'agent' | 'gear'>('agent')
  const [answerSchemaText, setAnswerSchemaText] = useState('')
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

    // Only what was actually filled in goes into the block: a task that
    // requires nothing must send nothing, because that is what makes it behave
    // exactly as tasks did before any of this existed.
    const expect: InletExpect = {}
    if (runsGear.trim()) expect.runs_gear = runsGear.trim()
    if (producesFiles.trim()) {
      const n = Number(producesFiles)
      if (!Number.isInteger(n) || n < 0) {
        onError(`"at least this many files" is a whole number of files, and ${producesFiles} is not one`)
        return
      }
      if (n > 0) expect.produces_files = n
    }
    if (answerFrom === 'gear') expect.answer_from = 'gear'
    if (answerSchemaText.trim()) {
      try {
        expect.schema = JSON.parse(answerSchemaText)
      } catch (err) {
        onError(`the answer schema is not valid JSON: ${err instanceof Error ? err.message : String(err)}`)
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
        expect: Object.keys(expect).length > 0 ? expect : undefined,
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
          <span className="row spread">
            <span className="muted">schema — what a caller's body must look like</span>
            {/* A button rather than a placeholder. The example used to BE the
                placeholder, which meant it vanished the moment anybody typed —
                the one instant it was needed. An operator writing their first
                schema had a grey example they could not copy, could not edit,
                and could not get back. */}
            <button type="button" className="link" onClick={() => setSchemaText(SCHEMA_STARTER)}>
              start from an example
            </button>
          </span>
          <textarea
            rows={12}
            spellCheck={false}
            value={schemaText}
            onChange={(e) => setSchemaText(e.target.value)}
            placeholder={'empty accepts any JSON body'}
          />
          {schemaText.trim() !== '' && schemaProblem(schemaText) && (
            // Said here, while they are looking at it, rather than on submit.
            <span className="hint danger">{schemaProblem(schemaText)}</span>
          )}
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

      <details className="inlet-expect-form">
        <summary>what has to have happened for this to count</summary>
        <p className="hint">
          Every field here is optional, and a task with none behaves exactly as it would without this section. What you
          put here is checked against the RECORD of the run — which tools were called and how each ended, which files
          appeared — and never against what the agent wrote. An agent cannot satisfy any of it by saying it did.
        </p>

        <label className="field">
          <span className="muted">a gear that must have run and succeeded</span>
          <input
            placeholder="unpack"
            value={runsGear}
            onChange={(e) => setRunsGear(e.target.value)}
          />
          <span className="hint">
            The gear’s own name, not the <code>gear_</code> tool name a model calls it by. It has to be forged already:
            a name nothing answers to would refuse every delivery, so it is refused here instead.
          </span>
        </label>

        <label className="field">
          <span className="muted">at least this many files must appear</span>
          <input
            type="number"
            min={0}
            step={1}
            placeholder="0"
            value={producesFiles}
            onChange={(e) => setProducesFiles(e.target.value)}
          />
          <span className="hint">
            Counts every file that appeared, including one the agent wrote itself — so this says work landed on disk,
            not that a particular gear ran. The field above is the one that answers that.
          </span>
        </label>

        <label className="field">
          <span className="muted">what the caller gets back</span>
          <select value={answerFrom} onChange={(e) => setAnswerFrom(e.target.value as 'agent' | 'gear')}>
            <option value="agent">the agent’s own answer</option>
            <option value="gear">the output of the last gear that succeeded</option>
          </select>
          <span className="hint">
            For a deterministic job the model is a router and its retelling of a gear’s output is one paraphrase away
            from being wrong. Taking the gear’s output means the prose is not returned at all — and a delivery in which
            no gear ran is a failure rather than an empty answer.
          </span>
        </label>

        <label className="field">
          <span className="muted">the shape the answer must have — leave it empty to accept anything</span>
          <textarea
            rows={5}
            value={answerSchemaText}
            onChange={(e) => setAnswerSchemaText(e.target.value)}
            placeholder={'{\n  "type": "object",\n  "required": ["bucket"],\n  "properties": {\n    "bucket": { "enum": ["invoice", "receipt"] }\n  }\n}'}
          />
          <span className="hint">
            The same JSON Schema subset as the payload above, and the same validator. An answer that does not fit ends
            the run as <code>refused_output_schema</code> — kept apart from the checks above because “the model produced
            garbage” and “the work did not happen” want different people woken up.
          </span>
        </label>
      </details>

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
        {/* Every underscore, not the first one: "refused output_schema" is a
            state nobody has, and it is the one an operator reads while trying
            to work out which of two refusals they are looking at. */}
        <span className={`status ${run.state}`}>{run.state.replace(/_/g, ' ')}</span>
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
        <RunRecordView did={run.did} />
      </div>
    </details>
  )
}

// What the run actually did, which is the part an operator diagnosing a failure
// at 3am needs before the prose — the prose is what cannot be trusted, and it is
// already above.
function RunRecordView({ did }: { did: InletRunRecord | null }) {
  if (!did) {
    return (
      <p className="hint">
        No record was kept for this run: it has not settled yet, or it came through before the record existed. That is
        not the same as a record showing nothing happened.
      </p>
    )
  }
  return (
    <div className="inlet-did">
      <p className="muted">what this run did</p>
      <ul>
        {did.tools.length === 0 ? (
          // The empty list is the answer, not a gap in the page. This is the
          // exact shape of the failure the record exists for: an agent that
          // described work it never asked anything to do.
          <li className="run-failed">no tool calls at all — nothing was run during this delivery</li>
        ) : (
          did.tools.map((t, i) => (
            <li key={`${t.name}-${i}`} className={t.ok ? undefined : 'run-failed'}>
              <code>{t.name}</code> {t.ok ? 'ok' : 'failed'} · {t.ms}ms
            </li>
          ))
        )}
      </ul>
      <ul>
        {did.files.length === 0 ? (
          <li className="muted">no files appeared</li>
        ) : (
          did.files.map((f) => (
            <li key={f.path}>
              <code>{f.path}</code> <span className="muted">{size(f.bytes)}</span>
            </li>
          ))
        )}
      </ul>
      <p className="muted">
        {did.model_calls} {did.model_calls === 1 ? 'model call' : 'model calls'} · {did.tokens.in} tokens in ·{' '}
        {did.tokens.out} out
      </p>
    </div>
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
