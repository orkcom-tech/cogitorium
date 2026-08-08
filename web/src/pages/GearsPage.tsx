import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Gear, type GearFile } from '../api'

export default function GearsPage() {
  const [gears, setGears] = useState<Gear[]>([])
  const [query, setQuery] = useState('')
  const [openId, setOpenId] = useState<number | null>(null)
  const [source, setSource] = useState<GearFile[]>([])
  const [error, setError] = useState<string | null>(null)
  const seqRef = useRef(0)

  const reload = useCallback(() => {
    // Per-keystroke searches may resolve out of order; only the latest wins.
    const seq = ++seqRef.current
    api.gears
      .list(query)
      .then((g) => {
        if (seqRef.current === seq) setGears(g)
      })
      .catch((e: Error) => {
        if (seqRef.current === seq) setError(e.message)
      })
  }, [query])

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

  const setStatus = (g: Gear, status: 'approved' | 'disabled') =>
    api.gears
      .setStatus(g.id, status)
      .then(reload)
      .catch((e: Error) => setError(e.message))

  const pending = gears.filter((g) => g.status === 'pending').length

  return (
    <div className="page">
      <h2>Gears</h2>
      <p className="hint">
        Tools your agents forged, kept for reuse. A gear runs as a subprocess with your own privileges — read the
        source before approving it. Nothing runs until you do.
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
      </div>
      {gears.length === 0 ? (
        <p className="hint">
          The catalog is empty. Agents forge gears on their own when they need a capability that doesn't exist yet.
        </p>
      ) : (
        gears.map((g) => (
          <div key={g.id} className="card">
            <div className="card-head">
              <strong>{g.name}</strong>
              <span className={`status ${g.status}`}>{g.status}</span>
              <span className="muted">
                v{g.version} · {g.runtime} · from {g.origin_workspace || 'unknown'}
                {g.created_by_agent ? ` / ${g.created_by_agent}` : ''}
              </span>
              <span className="spacer" />
              <button onClick={() => open(g)}>{openId === g.id ? 'hide source' : 'review source'}</button>
              {g.status !== 'approved' && <button onClick={() => setStatus(g, 'approved')}>approve</button>}
              {g.status === 'approved' && <button onClick={() => setStatus(g, 'disabled')}>disable</button>}
              <button
                className="danger"
                onClick={() => {
                  if (confirm(`Delete gear "${g.name}" and all its bindings?`))
                    api.gears.remove(g.id).then(reload).catch((e: Error) => setError(e.message))
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
            {openId === g.id && (
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
              </>
            )}
          </div>
        ))
      )}
    </div>
  )
}
