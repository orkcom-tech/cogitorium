import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Gear, type GearFile, type GearRun, type GearRunResult, gearRunStream } from '../api'
import { collapse, diff, stat, tooBig } from './diff'

export default function GearsPage() {
  const [gears, setGears] = useState<Gear[]>([])
  const [query, setQuery] = useState('')
  const [tag, setTag] = useState('')
  const [openId, setOpenId] = useState<number | null>(null)
  const [source, setSource] = useState<GearFile[]>([])
  const [authoring, setAuthoring] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Whether this server can actually isolate a gear; null until it answers.
  const [sandboxed, setSandboxed] = useState<boolean | null>(null)
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

  // Whether this server can actually isolate a gear. The terminal endpoint
  // already reports the backend, and it is the same backend gears run in.
  useEffect(() => {
    api.terminal
      .status()
      .then((t) => setSandboxed(!!t.backend && t.backend !== 'none'))
      .catch(() => setSandboxed(null))
  }, [])

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
      {/* This paragraph is a security claim, so it states what is actually
          true of THIS server rather than a general sentence. It said "runs as
          a subprocess with your own privileges" long after the sandbox landed,
          which is the wrong way round: it understated the protection here and
          would understate the danger on a server without one. */}
      <p className="hint">
        Tools your agents forged, kept for reuse.{' '}
        {sandboxed === null
          ? 'Read the source, and try it with the dry run, before approving it.'
          : sandboxed
            ? 'Each gear runs in a throwaway container with no network and none of this server\u2019s files — read the source, and try it with the dry run, before approving it.'
            : 'This server has no sandbox, so a gear runs as a subprocess with the server\u2019s own file access — read the source before approving anything.'}{' '}
        Nothing an agent calls runs until you do.
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
  // What the gear has printed so far. Kept separate from the final result: the
  // result is the record, this is the view of it arriving.
  const [live, setLive] = useState('')
  const abort = useRef<AbortController | null>(null)
  const [runs, setRuns] = useState<GearRun[] | null>(null)
  // The previous version's source, fetched only when asked for. An approval
  // covers exact content, so "what changed since the version I approved" is
  // the question this answers.
  const [prev, setPrev] = useState<GearFile[] | null>(null)
  const [comparing, setComparing] = useState(false)

  const compare = () => {
    if (comparing) {
      setComparing(false)
      return
    }
    if (prev) {
      setComparing(true)
      return
    }
    api.gears
      .get(g.id, g.version - 1)
      .then((r) => {
        setPrev(r.files)
        setComparing(true)
      })
      .catch((e: Error) => onError(e.message))
  }

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
    setDryResult(null)
    setLive('')
    const ac = new AbortController()
    abort.current = ac
    gearRunStream(
      g.id,
      parsed,
      (ev) => {
        // Both streams go into one pane, in the order they actually arrived.
        // Splitting them puts an error before the line that caused it, which
        // is the one ordering that makes a run harder to read.
        if (ev.type === 'output') setLive((t) => t + ev.text)
        if (ev.type === 'result') setDryResult(ev)
      },
      ac.signal,
    )
      .catch((e: Error) => {
        if (!ac.signal.aborted) onError(e.message)
      })
      .finally(() => {
        abort.current = null
        setRunning(false)
      })
  }

  // A run outlives the panel otherwise: the request keeps streaming into a
  // component nobody is looking at.
  useEffect(() => () => abort.current?.abort(), [])

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
          {comparing &&
            prev
              ?.filter((old) => !source.some((f) => f.path === old.path))
              .map((old) => (
                <details key={'gone-' + old.path} open>
                  <summary>
                    <code>{old.path}</code>
                    <span className="muted"> — removed in v{g.version}</span>
                  </summary>
                  <GearFileDiff before={old.content} after="" />
                </details>
              ))}
          {g.version > 1 && (
            <div className="row">
              <button onClick={compare}>
                {comparing ? `showing changes from v${g.version - 1}` : `compare with v${g.version - 1}`}
              </button>
              <span className="muted">
                an approval covers exact content, so this is what changed since the version you last saw
              </span>
            </div>
          )}
          {source.map((f) => (
            <details key={f.path} open={f.path === g.entrypoint && f.encoding !== 'base64'}>
              <summary>
                <code>{f.path}</code>
                {f.path === g.entrypoint && <span className="muted"> — entrypoint</span>}
                {f.encoding === 'base64' && <span className="muted"> — binary</span>}
              </summary>
              {f.encoding === 'base64' ? (
                // Showing megabytes of base64 helps nobody, and pretending a
                // blob is reviewable source would be worse: say plainly that
                // this one cannot be read before approving it.
                <p className="hint">
                  {Math.round((f.content.length * 3) / 4 / 1024).toLocaleString()} KB of compiled code — it cannot
                  be read here. Approve it only if you trust where it came from, and use the dry run below to see
                  what it does.
                </p>
              ) : comparing ? (
                <GearFileDiff before={prev?.find((x) => x.path === f.path)?.content ?? ''} after={f.content} />
              ) : (
                <pre className="prompt-preview">{f.content}</pre>
              )}
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
              {running && (
                <button onClick={() => abort.current?.abort()} title="Stop watching; the gear finishes on the server">
                  stop watching
                </button>
              )}
            </div>
          </div>
          {/* While it runs, this is the only thing on screen; once the result
              lands it is replaced by the recorded run, which is the same bytes
              plus the exit code. */}
          {running && (
            <pre className="prompt-preview run-live">{live || 'waiting for the first output…'}</pre>
          )}
          {!running && dryResult && (
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

// A gear can be written here or brought in from disk — a single script, a
// set of files, or a compiled executable. All three land pending and go
// through the same review as anything an agent forged.
function AuthorGearForm({ onDone, onError }: { onDone: () => void; onError: (m: string) => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [runtime, setRuntime] = useState('python')
  const [tags, setTags] = useState('')
  const [source, setSource] = useState<'write' | 'upload'>('write')
  const [code, setCode] = useState('import sys, json\nargs = json.load(sys.stdin)\nprint(args)\n')
  const [files, setFiles] = useState<GearFile[]>([])
  const [entrypoint, setEntrypoint] = useState('')

  const readFiles = async (list: FileList) => {
    const read = await Promise.all(
      Array.from(list).map(
        (f) =>
          new Promise<GearFile>((resolve, reject) => {
            const reader = new FileReader()
            reader.onerror = () => reject(new Error(`could not read ${f.name}`))
            reader.onload = () => {
              const buf = new Uint8Array(reader.result as ArrayBuffer)
              // Anything with a NUL byte is not source; carry it as base64
              // so it survives the round trip intact.
              const binary = buf.includes(0)
              if (binary) {
                let s = ''
                buf.forEach((b) => (s += String.fromCharCode(b)))
                resolve({ path: f.name, content: btoa(s), encoding: 'base64' })
              } else {
                resolve({ path: f.name, content: new TextDecoder().decode(buf), encoding: 'utf8' })
              }
            }
            reader.readAsArrayBuffer(f)
          }),
      ),
    ).catch((e: Error) => {
      onError(e.message)
      return [] as GearFile[]
    })
    setFiles(read)
    if (read.length === 1) setEntrypoint(read[0].path)
    if (read.some((f) => f.encoding === 'base64')) setRuntime('binary')
  }

  return (
    <form
      className="card"
      onSubmit={(e) => {
        e.preventDefault()
        const payload = {
          name,
          description,
          runtime,
          tags: tags.split(',').map((t) => t.trim()).filter(Boolean),
          ...(source === 'write' ? { code } : { files, entrypoint }),
        }
        api.gears
          .create(payload)
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
          <option value="binary">executable</option>
        </select>
        <input
          className="grow"
          placeholder="what it does — agents read this"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <input placeholder="tags, comma separated" value={tags} onChange={(e) => setTags(e.target.value)} />
      </div>

      <div className="tabs">
        <button type="button" className={source === 'write' ? 'active' : ''} onClick={() => setSource('write')}>
          write it
        </button>
        <button type="button" className={source === 'upload' ? 'active' : ''} onClick={() => setSource('upload')}>
          bring files
        </button>
      </div>

      {source === 'write' ? (
        <label className="field">
          <span className="muted">the program — reads JSON arguments on stdin, prints its result to stdout</span>
          <textarea rows={10} value={code} onChange={(e) => setCode(e.target.value)} spellCheck={false} />
        </label>
      ) : (
        <>
          <label className="field">
            <span className="muted">files from disk — scripts, data, or a compiled executable</span>
            <input
              type="file"
              multiple
              onChange={(e) => {
                if (e.target.files?.length) void readFiles(e.target.files)
              }}
            />
          </label>
          {files.length > 0 && (
            <>
              {files.map((f) => (
                <div key={f.path} className="row binding">
                  <code>{f.path}</code>
                  <span className="muted">
                    {f.encoding === 'base64' ? 'binary' : `${f.content.length} chars`}
                  </span>
                </div>
              ))}
              <label className="field">
                <span className="muted">which of them runs</span>
                <select value={entrypoint} onChange={(e) => setEntrypoint(e.target.value)}>
                  <option value="">pick the entrypoint…</option>
                  {files.map((f) => (
                    <option key={f.path} value={f.path}>
                      {f.path}
                    </option>
                  ))}
                </select>
              </label>
            </>
          )}
          {runtime === 'binary' && (
            <p className="hint">
              An executable runs inside the sandbox container, so it must be built for Linux and the container's
              architecture — a build for this Mac will not start there.
            </p>
          )}
        </>
      )}

      <div className="row">
        <button type="submit" disabled={source === 'upload' && (files.length === 0 || !entrypoint)}>
          create (lands pending, like a forged one)
        </button>
      </div>
    </form>
  )
}

/**
 * One file, before and after.
 *
 * A gear's approval covers exact content, so the useful question about a new
 * version is not "what does it do" but "what is different from the one I
 * already read". Same renderer as the file editor's diff — one algorithm, one
 * set of colours, so the two never disagree about what a change looks like.
 */
function GearFileDiff({ before, after }: { before: string; after: string }) {
  if (before === after) return <p className="hint">unchanged in this version</p>
  if (tooBig(before, after)) return <p className="hint">too large to compare line by line</p>
  const rows = diff(before, after)
  const s = stat(rows)
  const { rows: shown, gapAfter } = collapse(rows)
  return (
    <>
      <p className="hint">
        <span className="d-add">+{s.added}</span> <span className="d-del">−{s.removed}</span>
        {before === '' && ' · new file'}
        {after === '' && ' · file removed'}
      </p>
      <pre className="prompt-preview gear-diff">
        {shown.map((r, i) => (
          <span key={i}>
            <span className={`dl dl-${r.op}`}>
              <span className="dl-n">{r.left ?? ''}</span>
              <span className="dl-n">{r.right ?? ''}</span>
              <span className="dl-s">{r.op === 'add' ? '+' : r.op === 'del' ? '\u2212' : ' '}</span>
              <span className="dl-t">{r.text || ' '}</span>
            </span>
            {gapAfter.has(i) && <span className="dl dl-gap">\u22ef</span>}
          </span>
        ))}
      </pre>
    </>
  )
}
