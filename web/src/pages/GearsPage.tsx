import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Gear, type GearFile, type GearRun, type GearRunResult } from '../api'

export default function GearsPage() {
  const [gears, setGears] = useState<Gear[]>([])
  const [query, setQuery] = useState('')
  const [tag, setTag] = useState('')
  const [openId, setOpenId] = useState<number | null>(null)
  const [source, setSource] = useState<GearFile[]>([])
  const [authoring, setAuthoring] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seqRef = useRef(0)

  const reload = useCallback(() => {
    // Per-keystroke searches may resolve out of order; only the latest wins.
    const seq = ++seqRef.current
    api.gears
      .list(query, tag)
      .then((g) => {
        if (seqRef.current === seq) setGears(g)
      })
      .catch((e: Error) => {
        if (seqRef.current === seq) setError(e.message)
      })
  }, [query, tag])

  useEffect(reload, [reload])

  const open = (g: Gear) => {
    if (openId === g.id) {
      setOpenId(null)
      return
    }
    api.gears
      .get(g.id)
      .then((r) => {
        setOpenId(g.id)
        setSource(r.files)
      })
      .catch((e: Error) => setError(e.message))
  }

  const allTags = [...new Set(gears.flatMap((g) => g.tags))].sort()
  const pending = gears.filter((g) => g.status === 'pending').length

  return (
    <div className="page">
      <h2>Gears</h2>
      <p className="hint">
        Tools your agents forged, kept for reuse. A gear runs as a subprocess with your own privileges — read the
        source, and try it with the dry run, before approving it. Nothing an agent calls runs until you do.
      </p>
      {pending > 0 && (
        <p className="notice">
          {pending} gear{pending > 1 ? 's' : ''} awaiting your review.
        </p>
      )}
      {error && <p className="error">{error}</p>}
      <div className="row">
        <input
          className="grow"
          placeholder="search by name or description…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <select value={tag} onChange={(e) => setTag(e.target.value)}>
          <option value="">all tags</option>
          {allTags.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <button onClick={() => setAuthoring((v) => !v)}>{authoring ? 'cancel' : 'write a gear'}</button>
      </div>
      {authoring && (
        <AuthorGearForm
          onDone={() => {
            setAuthoring(false)
            reload()
          }}
          onError={setError}
        />
      )}
      {gears.length === 0 ? (
        <p className="hint">
          Nothing here yet. Agents forge gears when they need a capability that doesn't exist — or you can write one
          yourself.
        </p>
      ) : (
        gears.map((g) => (
          <GearCard
            key={g.id}
            gear={g}
            open={openId === g.id}
            source={openId === g.id ? source : []}
            onToggle={() => open(g)}
            onChanged={reload}
            onError={setError}
          />
        ))
      )}
    </div>
  )
}

function GearCard({
  gear: g,
  open,
  source,
  onToggle,
  onChanged,
  onError,
}: {
  gear: Gear
  open: boolean
  source: GearFile[]
  onToggle: () => void
  onChanged: () => void
  onError: (m: string) => void
}) {
  const [dryArgs, setDryArgs] = useState('{}')
  const [dryResult, setDryResult] = useState<GearRunResult | null>(null)
  const [running, setRunning] = useState(false)
  const [runs, setRuns] = useState<GearRun[] | null>(null)

  const setStatus = (status: 'approved' | 'disabled') =>
    api.gears.setStatus(g.id, status).then(onChanged).catch((e: Error) => onError(e.message))

  const dryRun = () => {
    let parsed: unknown
    try {
      parsed = JSON.parse(dryArgs || '{}')
    } catch {
      onError('arguments must be valid JSON')
      return
    }
    setRunning(true)
    api.gears
      .run(g.id, parsed)
      .then(setDryResult)
      .catch((e: Error) => onError(e.message))
      .finally(() => setRunning(false))
  }

  return (
    <div className="card">
      <div className="card-head">
        <strong>{g.name}</strong>
        <span className={`status ${g.status}`}>{g.status}</span>
        <span className="muted">
          v{g.version} · {g.runtime} · {g.created_by_agent ? `forged by ${g.created_by_agent}` : 'written by you'}
          {g.origin_workspace ? ` in ${g.origin_workspace}` : ''}
        </span>
        <span className="spacer" />
        <button onClick={onToggle}>{open ? 'hide' : 'review & run'}</button>
        {g.status !== 'approved' && <button onClick={() => setStatus('approved')}>approve</button>}
        {g.status === 'approved' && <button onClick={() => setStatus('disabled')}>disable</button>}
        <button
          className="danger"
          onClick={() => {
            if (confirm(`Delete gear "${g.name}" and all its bindings?`))
              api.gears.remove(g.id).then(onChanged).catch((e: Error) => onError(e.message))
          }}
        >
          delete
        </button>
      </div>
      <p className="gear-desc">{g.description}</p>
      {g.tags.length > 0 && (
        <div className="pick-list">
          {g.tags.map((t) => (
            <code key={t} className="chip">
              {t}
            </code>
          ))}
        </div>
      )}

      {open && (
        <>
          {source.map((f) => (
            <details key={f.path} open={f.path === g.entrypoint}>
              <summary>
                <code>{f.path}</code>
                {f.path === g.entrypoint && <span className="muted"> — entrypoint</span>}
              </summary>
              <pre className="prompt-preview">{f.content}</pre>
            </details>
          ))}
          <details>
            <summary>arguments schema</summary>
            <pre className="prompt-preview">{g.args_schema}</pre>
          </details>

          <div className="field">
            <span className="muted">
              dry run — executes the gear right now, even while pending, so approval is an informed decision
            </span>
            <div className="row">
              <input
                className="grow"
                value={dryArgs}
                onChange={(e) => setDryArgs(e.target.value)}
                placeholder='arguments as JSON, e.g. {"numbers": [1, 2, 3]}'
              />
              <button onClick={dryRun} disabled={running}>
                {running ? 'running…' : 'run'}
              </button>
            </div>
          </div>
          {dryResult && (
            <pre className={`prompt-preview ${dryResult.exit_code !== 0 || dryResult.error ? 'run-failed' : ''}`}>
              {dryResult.error
                ? dryResult.error
                : `exit ${dryResult.exit_code}${dryResult.timed_out ? ' (timed out)' : ''}\n${dryResult.stdout}${
                    dryResult.stderr ? `\nstderr:\n${dryResult.stderr}` : ''
                  }`}
            </pre>
          )}

          <div className="row">
            <span className="muted">timeout</span>
            <input
              type="number"
              min={1}
              max={3600}
              defaultValue={g.timeout_seconds}
              onBlur={(e) => {
                const v = Number(e.target.value)
                if (v && v !== g.timeout_seconds)
                  api.gears.setTimeout(g.id, v).then(onChanged).catch((err: Error) => onError(err.message))
              }}
            />
            <span className="muted">seconds</span>
            <span className="spacer" />
            <button
              onClick={() =>
                runs === null
                  ? api.gears.runs(g.id).then(setRuns).catch((e: Error) => onError(e.message))
                  : setRuns(null)
              }
            >
              {runs === null ? 'execution history' : 'hide history'}
            </button>
          </div>

          {runs !== null &&
            (runs.length === 0 ? (
              <p className="hint">This gear has never run.</p>
            ) : (
              <table>
                <thead>
                  <tr>
                    <th>when</th>
                    <th>by</th>
                    <th>args</th>
                    <th>exit</th>
                    <th>took</th>
                  </tr>
                </thead>
                <tbody>
                  {runs.map((r) => (
                    <tr key={r.id}>
                      <td className="muted">{r.created_at}</td>
                      <td>{r.agent_name || 'you (dry run)'}</td>
                      <td>
                        <code>{r.args.slice(0, 40)}</code>
                      </td>
                      <td className={r.exit_code === 0 ? '' : 'error'}>
                        {r.exit_code}
                        {r.timed_out ? ' ⏱' : ''}
                      </td>
                      <td className="muted">{r.duration_ms} ms</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ))}
        </>
      )}
    </div>
  )
}

function AuthorGearForm({ onDone, onError }: { onDone: () => void; onError: (m: string) => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [runtime, setRuntime] = useState('python')
  const [tags, setTags] = useState('')
  const [code, setCode] = useState(
    'import sys, json\nargs = json.load(sys.stdin)\nprint(args)\n',
  )

  return (
    <form
      className="card"
      onSubmit={(e) => {
        e.preventDefault()
        api.gears
          .create({
            name,
            description,
            runtime,
            code,
            tags: tags
              .split(',')
              .map((t) => t.trim())
              .filter(Boolean),
          })
          .then(onDone)
          .catch((err: Error) => onError(err.message))
      }}
    >
      <div className="row">
        <input required placeholder="name, e.g. csv_summarize" value={name} onChange={(e) => setName(e.target.value)} />
        <select value={runtime} onChange={(e) => setRuntime(e.target.value)}>
          <option value="python">python</option>
          <option value="node">node</option>
          <option value="bash">bash</option>
        </select>
        <input
          className="grow"
          placeholder="what it does — agents read this"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <input placeholder="tags, comma separated" value={tags} onChange={(e) => setTags(e.target.value)} />
      </div>
      <label className="field">
        <span className="muted">the program — reads JSON arguments on stdin, prints its result to stdout</span>
        <textarea rows={10} value={code} onChange={(e) => setCode(e.target.value)} spellCheck={false} />
      </label>
      <div className="row">
        <button type="submit">create (lands pending, like a forged one)</button>
      </div>
    </form>
  )
}
