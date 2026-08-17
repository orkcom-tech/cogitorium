import { useCallback, useEffect, useRef, useState } from 'react'
import { Select } from './Select'
import { Field, Fields } from './Field'
import {
  api,
  type EnvStatus,
  type Gear,
  type GearConnection,
  type GearFile,
  type GearRun,
  type GearRunResult,
  type User,
  gearRunStream,
} from '../api'
import { collapse, diff, stat, tooBig } from './diff'
import { dragging } from '../dnd'

export default function GearsPage({ me, review }: { me: User; review?: number | null }) {
  const [gears, setGears] = useState<Gear[]>([])
  const [query, setQuery] = useState('')
  const [tag, setTag] = useState('')
  const [openId, setOpenId] = useState<number | null>(null)
  const [source, setSource] = useState<GearFile[]>([])
  // What each declared name would resolve to on this install. It arrives with
  // the source, in the same response, because it is half of the same decision.
  const [env, setEnv] = useState<EnvStatus[]>([])
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

  // Opened from somewhere else — a gear dropped on the blueprint before anyone
  // approved it, and the note on the canvas offered to bring the operator
  // here. It opens the card rather than approving anything: approval still
  // happens beside the source and the grants, which is the whole point of this
  // screen.
  useEffect(() => {
    if (!review) return
    setOpenId(review)
    api.gears
      .get(review)
      .then((r) => {
        setSource(r.files)
        setEnv(r.env)
      })
      .catch((e: Error) => setError(e.message))
  }, [review])

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
        setEnv(r.env ?? [])
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
            ? 'Each gear runs in a throwaway container with none of this server\u2019s files, and no network unless you grant it one when you approve it — read the source, and try it with the dry run, first.'
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
        <Select
          value={tag}
          aria-label="Filter by tag"
          placeholder="all tags"
          onChange={setTag}
          options={[{ value: '', label: 'all tags' }, ...allTags.map((t) => ({ value: t, label: t }))]}
        />
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
            me={me}
            open={openId === g.id}
            source={openId === g.id ? source : []}
            env={openId === g.id ? env : []}
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
  me,
  open,
  source,
  env,
  onToggle,
  onChanged,
  onError,
}: {
  gear: Gear
  me: User
  open: boolean
  source: GearFile[]
  env: EnvStatus[]
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
  const [conns, setConns] = useState<GearConnection[] | null>(null)
  // The grant as this screen currently states it. It starts as what the gear
  // holds and becomes what the operator is about to decide — the dry run below
  // uses it too, so what they judge is what they are about to allow.
  const [netOn, setNetOn] = useState(g.network_granted)
  const [hostsText, setHostsText] = useState(g.network_hosts.join('\n'))
  const isAdmin = me.role === 'admin'
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

  // The gear's own grant is the truth; the fields follow it when it changes
  // under them — after a save here, or after an agent forged a new version,
  // which withdraws the grant along with the approval.
  const granted = g.network_granted
  const grantedHosts = g.network_hosts.join('\n')
  useEffect(() => {
    setNetOn(granted)
    setHostsText(grantedHosts)
  }, [granted, grantedHosts])

  const network = () => ({
    granted: netOn,
    hosts: hostsText.split('\n').map((h) => h.trim()).filter(Boolean),
  })
  const netChanged = netOn !== granted || network().hosts.join('\n') !== grantedHosts

  const setStatus = (status: 'approved' | 'disabled') =>
    // Approving carries the grant with it: they are one decision, and between
    // two requests there is a moment when the code runs and the operator has
    // not finished saying what it may do.
    api.gears
      .setStatus(g.id, status, isAdmin ? network() : undefined)
      .then(onChanged)
      .catch((e: Error) => onError(e.message))

  const saveNetwork = () =>
    api.gears.setNetwork(g.id, network()).then(onChanged).catch((e: Error) => onError(e.message))

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
      // Only an administrator may try a gear WITH the network, for the same
      // reason only an administrator may grant it: a dry run is open to anyone
      // signed in because the sandbox is the blast radius, and the network is
      // half of that radius.
      isAdmin && netOn ? network() : undefined,
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
    <div
      className="card gear-card-drag"
      // Draggable only while collapsed. Open, this card contains a textarea of
      // source and a JSON field for the dry run, and a draggable ancestor
      // turns selecting text in them into dragging the card.
      draggable={!open}
      onDragStart={dragging({ kind: 'gear', id: g.id, name: g.name, status: g.status })}
      title={open ? undefined : 'Drag onto the blueprint to give this gear to an agent'}
    >
      <div className="card-head">
        <strong>{g.name}</strong>
        <span className={`status ${g.status}`}>{g.status}</span>
        <span className="muted">
          v{g.version} · {g.runtime} · {g.created_by_agent ? `forged by ${g.created_by_agent}` : 'written by you'}
          {g.origin_workspace ? ` in ${g.origin_workspace}` : ''}
        </span>
        <span className="spacer" />
        {/* Approving is deliberately NOT here. It lives below, beside the two
            things it grants — an approval given from a collapsed card is given
            without seeing which credentials this code gets or whether it may
            reach out, and that is the decision this whole screen exists to
            make possible. Same single click, moved to where the facts are. */}
        {/* On a pending gear this is the primary action of the card and it is
            styled like one. It was a plain button in a row of plain buttons,
            indistinguishable from "delete" — on the one screen whose entire
            purpose is deciding whether agent-written code may run, the way in
            had to be hunted for.

            Loud here, considered inside: this button only OPENS the review.
            The approval itself is still below, beside the credentials and the
            network grant it confers, so nothing is ever approved from a
            collapsed card. */}
        <button className={g.status === 'pending' && !open ? 'primary' : ''} onClick={onToggle}>
          {open ? 'hide' : g.status === 'approved' ? 'review & run' : 'review & approve'}
        </button>
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
      {/* Both grants in one line, so the catalogue can be scanned for the gears
          that hold something without opening each one. */}
      <p className="gear-grants">
        <span className={g.env_names.length > 0 ? 'grant-on' : 'muted'}>
          ⚿ {g.env_names.length > 0 ? g.env_names.join(', ') : 'no credentials'}
        </span>
        <span className="grant-sep">·</span>
        <span className={g.network_granted ? 'grant-on' : 'muted'}>
          ⇄{' '}
          {!g.network_granted
            ? 'no network'
            : g.network_hosts.length > 0
              ? g.network_hosts.join(', ')
              : 'the network, anywhere'}
        </span>
      </p>
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

          {/* The other half of the approval, in the same place as the source.
              An operator can only be responsible for what they can see, and
              "this code" and "what this code is given" shown apart is a
              decision made blind. */}
          <div className="grants">
            <h4>What approving this grants</h4>

            <div className="grant">
              <span className="grant-label">credentials</span>
              {g.env_names.length === 0 ? (
                <p className="muted">
                  None. This gear is given no named values, and its environment holds nothing but its own name.
                </p>
              ) : (
                <ul className="grant-list">
                  {g.env_names.map((n) => {
                    const st = env.find((e) => e.name === n)
                    return (
                      <li key={n}>
                        <code>{n}</code>{' '}
                        {!st ? (
                          <span className="muted">…</span>
                        ) : st.found ? (
                          <span className={st.kind === 'secret' ? 'env-secret' : 'muted'}>
                            {st.kind} · from {st.source}
                          </span>
                        ) : (
                          // Learning this at approval beats learning it at
                          // three in the morning from a run that refused.
                          <span className="error">
                            nothing on this install supplies it, so every run of this gear will be refused
                          </span>
                        )}
                      </li>
                    )
                  })}
                </ul>
              )}
            </div>

            <div className="grant">
              <span className="grant-label">network</span>
              <label className="row grant-check">
                <input
                  type="checkbox"
                  checked={netOn}
                  disabled={!isAdmin}
                  onChange={(e) => setNetOn(e.target.checked)}
                />
                <span>this code may reach out</span>
              </label>
              {netOn && (
                <label className="field">
                  <span className="muted">
                    where — one host per line, <code>*.example.com</code> for any subdomain. Empty allows anywhere,
                    which is a choice you may make; a list is what makes the connection log below worth reading.
                  </span>
                  <textarea
                    rows={3}
                    spellCheck={false}
                    disabled={!isAdmin}
                    value={hostsText}
                    onChange={(e) => setHostsText(e.target.value)}
                    placeholder={'api.example.com\n*.internal.example.com'}
                  />
                </label>
              )}
            </div>

            {/* Stated rather than designed away, because it cannot be designed
                away: this is what granting both means, and the plan records it
                for exactly this moment. */}
            {netOn && g.env_names.length > 0 && (
              <p className="notice">
                This gear gets both. Code that holds a credential and can reach out can send that credential to
                wherever it is allowed to reach — nothing below this screen prevents that, and nothing can. Reading
                the source above is the control.
              </p>
            )}

            {/* The most consequential button in the product is the first one
                below: it is what lets agent-written code run on this machine.
                It is styled as the primary action because it IS the decision
                this screen exists to make — and it sits here, under the source
                and beside the grants it confers, so it can only be pressed by
                somebody who has been shown both. */}
            {isAdmin ? (
              <div className="row">
                {g.status !== 'approved' ? (
                  <button className="primary" onClick={() => setStatus('approved')}>
                    approve, with these grants
                  </button>
                ) : (
                  <button onClick={saveNetwork} disabled={!netChanged}>
                    {netChanged ? 'save the network grant' : 'network grant saved'}
                  </button>
                )}
                <span className="muted">
                  A new version of this gear returns to pending and gives both grants back — an approval covers exact
                  content.
                </span>
              </div>
            ) : (
              <p className="hint">Only an administrator can approve a gear or change what it may reach.</p>
            )}
          </div>

          <div className="field">
            <span className="muted">
              dry run — executes the gear right now, even while pending, so approval is an informed decision
            </span>
            <div className="row">
              <input
                className="grow"
                value={dryArgs}
                onChange={(e) => setDryArgs(e.target.value)}
                aria-label="Arguments"
                placeholder='{"numbers": [1, 2, 3]}'
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
            {/* The other half of the history: what this code reached. Kept
                beside the runs because "what did this gear do" is one question
                and the answer is both. */}
            <button
              onClick={() =>
                conns === null
                  ? api.gears.connections(g.id).then(setConns).catch((e: Error) => onError(e.message))
                  : setConns(null)
              }
            >
              {conns === null ? 'connections' : 'hide connections'}
            </button>
          </div>

          {conns !== null &&
            (conns.length === 0 ? (
              <p className="hint">
                This gear has never opened a connection. Every one it does open is written down before the socket is,
                whether or not it was allowed.
              </p>
            ) : (
              <table>
                <thead>
                  <tr>
                    <th>when</th>
                    <th>by</th>
                    <th>where</th>
                    <th>outcome</th>
                    <th>up</th>
                    <th>down</th>
                  </tr>
                </thead>
                <tbody>
                  {conns.map((c) => (
                    <tr key={c.id}>
                      <td className="muted">{c.created_at}</td>
                      <td>{c.agent_name || 'you (dry run)'}</td>
                      <td>
                        <code>
                          {c.host}:{c.port}
                        </code>{' '}
                        <span className="muted">{c.method}</span>
                      </td>
                      <td>
                        <span className={`status ${c.state}`} title={c.error}>
                          {c.state.replace(/_/g, ' ')}
                        </span>
                      </td>
                      <td className="muted">{bytes(c.bytes_sent)}</td>
                      <td className="muted">{bytes(c.bytes_received)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ))}

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
  // Names, never values. What each name means is set under Variables &
  // Secrets; whether this gear may reach out is decided at approval, on the
  // card, beside the source.
  const [envNames, setEnvNames] = useState('')
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
          env_names: envNames.split(',').map((n) => n.trim().toUpperCase()).filter(Boolean),
          ...(source === 'write' ? { code } : { files, entrypoint }),
        }
        api.gears
          .create(payload)
          .then(onDone)
          .catch((err: Error) => onError(err.message))
      }}
    >
      <Fields>
        <Field label="Name" hint="lower case with underscores — this is what an agent calls the tool by">
          <input required placeholder="csv_summarize" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Runtime" hint="what runs the file; executable takes a compiled binary">
          <Select
            value={runtime}
            aria-label="Runtime"
            onChange={setRuntime}
            options={[
              { value: 'python', label: 'python' },
              { value: 'node', label: 'node' },
              { value: 'bash', label: 'bash' },
              { value: 'binary', label: 'executable' },
            ]}
          />
        </Field>
        <Field
          wide
          label="Description"
          hint="an agent reads this to decide whether to reach for the tool, so write it for the agent"
        >
          <input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field label="Tags" hint="comma separated; the catalogue filters on these">
          <input placeholder="csv, reporting" value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </Fields>

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
                <Select
                  value={entrypoint}
                  aria-label="Entrypoint — which of the files runs"
                  placeholder="pick the entrypoint…"
                  onChange={setEntrypoint}
                  options={files.map((f) => ({ value: f.path, label: f.path }))}
                />
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

      <label className="field">
        <span className="muted">
          named values this gear reads from its environment, comma separated — e.g. API_KEY, BASE_URL. What each one
          means is set under Variables &amp; Secrets, and the gear never receives a value you type here.
        </span>
        <input placeholder="API_KEY, BASE_URL" value={envNames} onChange={(e) => setEnvNames(e.target.value)} />
      </label>

      <div className="row">
        <button type="submit" disabled={source === 'upload' && (files.length === 0 || !entrypoint)}>
          create (lands pending, like a forged one)
        </button>
        <span className="muted">
          Whether it may reach the network is decided when you approve it, beside its source.
        </span>
      </div>
    </form>
  )
}

// Bytes, in the shortest form that is still honest. A connection log is read
// for shape — "it sent 4 KB and got 2 MB back" — not for exact counts.
function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
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
