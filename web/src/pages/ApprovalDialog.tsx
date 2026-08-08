import { useEffect, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { ApprovalRequest } from '../api'

// The last control before data leaves the machine.
//
// Everything here is either fixed chrome or a server-computed fact. The only
// model-authored strings are the query and the agent name, and both are
// constrained server-side: the query to printable text under 256 runes, the
// name to a short slug. There is deliberately no free-text "reason" field —
// a box the attacker fills, displayed at the moment of decision, is a prompt
// injection aimed at the human.
export default function ApprovalDialog({
  request,
  onAnswer,
  busy,
}: {
  request: ApprovalRequest
  onAnswer: (allow: boolean) => void
  busy: boolean
}) {
  // Allow arms after a second. This is the pattern browsers use on permission
  // prompts, and it costs nothing while defeating a reflex click that was
  // already queued for whatever was under the cursor.
  const [armed, setArmed] = useState(false)
  const [left, setLeft] = useState(() => secondsLeft(request.expires_at))

  useEffect(() => {
    setArmed(false)
    const t = setTimeout(() => setArmed(true), 1000)
    return () => clearTimeout(t)
  }, [request.token])

  useEffect(() => {
    const t = setInterval(() => setLeft(secondsLeft(request.expires_at)), 500)
    return () => clearInterval(t)
  }, [request.expires_at])

  // Esc refuses, and so does any dismissal: the safe answer must be the one
  // that happens when the operator does nothing deliberate.
  //
  // Bound to the dialog element and stopped here, NOT to window. A second
  // global Escape handler anywhere in the app would mean one keypress both
  // closed a panel and refused a pending web search — the operator would see
  // the panel shut, learn nothing about the security decision made on their
  // behalf, and the turn would stop asking.
  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onAnswer(false)
    }
  }

  const f = request.facts
  const delegated = request.chain.length > 1

  return (
    <div className="modal-backdrop" onClick={() => onAnswer(false)}>
      <div
        className="modal approval"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
        role="dialog"
        aria-modal="true"
      >
        <h3>A search is about to leave this machine.</h3>

        <div className="approval-who">
          {/* Id first: ids are not spoofable, names are. */}
          <strong>
            agent #{request.agent_id} · {request.agent_name}
          </strong>
          <p className="muted role-excerpt">{request.role_excerpt}</p>
          <p className="muted">
            path: {request.chain.join(' → ')}
            {delegated && (
              <span className="warn"> · requested through a delegation, not by this agent directly</span>
            )}
          </p>
          {f.granted_by && (
            <p className="muted">
              granted by {f.granted_by}
              {f.granted_at ? ` · ${f.granted_at.slice(0, 10)}` : ''}
            </p>
          )}
        </div>

        <label className="approval-label">these words leave this machine</label>
        {/* The query itself, verbatim. It is the only part an agent authored
            and therefore the only part worth reading closely — the destination
            is a constant compiled into the binary, not something a prompt can
            steer. Plain text node: never dangerouslySetInnerHTML, never a
            markdown renderer, never syntax highlighting. */}
        <div className="wire" dir="ltr">
          {request.query}
        </div>

        <div className="approval-scroll">
        <div className="approval-facts">
          <span>
            {f.runes} runes · {f.bytes} bytes · {f.non_ascii} non-ASCII
          </span>
          {f.blob_shaped && <span className="warn">blob-shaped run detected</span>}
          {(f.overlap ?? []).map((o) => (
            <span key={o.source} className="warn">
              contains {o.chars} characters that also appear in {o.source}
            </span>
          ))}
          <span>
            request {f.used_this_turn} of {f.max_per_turn} permitted this turn
          </span>
          <span>
            this agent has sent {f.used_24h} of {f.max_24h} searches in 24h
          </span>
        </div>

        {(f.prev_queries ?? []).length > 0 && (
          <details className="approval-prev">
            <summary>what this agent usually searches for</summary>
            <ul>
              {(f.prev_queries ?? []).map((q, i) => (
                <li key={i}>{q}</li>
              ))}
            </ul>
          </details>
        )}

        </div>

        <p className="approval-warning">
          These exact words leave this machine when you allow — whether or not anything is found.
        </p>

        <div className="row approval-actions">
          <button className="danger" autoFocus onClick={() => onAnswer(false)} disabled={busy}>
            Refuse
          </button>
          <button className="primary" onClick={() => onAnswer(true)} disabled={!armed || busy}>
            {armed ? 'Allow once' : 'Allow once…'}
          </button>
          <span className="spacer" />
          <span className="muted">{left > 0 ? `${left}s left` : 'expiring…'}</span>
        </div>

        <p className="muted approval-foot">
          Refusing also stops this turn from asking again. If nobody answers, the search is refused and the turn
          continues.
        </p>
      </div>
    </div>
  )
}

function secondsLeft(iso: string) {
  const ms = new Date(iso).getTime() - Date.now()
  return Math.max(0, Math.round(ms / 1000))
}
