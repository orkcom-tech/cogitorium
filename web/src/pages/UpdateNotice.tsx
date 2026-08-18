import { useCallback, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { api, type UpdateProduct, type UpdateReport, type User } from '../api'

/**
 * Knowing there is a new version, without being nagged about it.
 *
 * Four rules, and the fourth is the one that decides whether the other three
 * are welcome or resented.
 *
 *   QUIET. Not a modal on arrival, not a banner across the work. The rail is
 *   where the product's own state already lives, so a version that has moved
 *   belongs beside it as a dot. Opening it is the operator's move.
 *
 *   WHAT CHANGED, not what number. "1.6.0 is out" is not a reason to update.
 *   The release notes are the reason, so they are what the panel shows.
 *
 *   AN HONEST WAY TO TAKE IT. Whoever owns the binary gets to replace it. If
 *   Homebrew put it there, the panel prints the brew command and nothing else;
 *   in a container it prints no command at all, because anything typed inside
 *   one is gone at the next start. This product never overwrites itself.
 *
 *   TOLD ONCE IS TOLD. A dismissal is remembered against the VERSION, so the
 *   dot comes back when there is something new to say and never for the thing
 *   already read. A notice that returns every day teaches people to dismiss it
 *   without reading, which is the exact state it exists to prevent.
 */

// Per operator, per device, per version. Not a server setting: "I have read
// this" is a fact about a person, and one operator dismissing it must not
// silence it for the rest of a team.
const SEEN = 'cogitorium.updateSeen'

function seen(): string[] {
  try {
    const raw = localStorage.getItem(SEEN)
    const v: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    // A browser with storage disabled shows the notice every time, which is
    // the harmless failure. Losing the notice would be the other one.
    return []
  }
}

/** What a report is offering, as one stable string, so a dismissal names it. */
function offer(report: UpdateReport | null): string {
  if (!report) return ''
  return report.products
    .filter((p) => p.newer && p.latest)
    .map((p) => `${p.name}@${p.latest!.tag}`)
    .sort()
    .join(' ')
}

export default function UpdateNotice({ me }: { me: User }) {
  const [report, setReport] = useState<UpdateReport | null>(null)
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [dismissed, setDismissed] = useState<string[]>(seen)

  const load = useCallback(
    () =>
      api.updates
        .status()
        .then(setReport)
        // Silent: an install where this cannot be read is an install that shows
        // no notice, which is the same as one with nothing to say. It is not
        // worth an error on a screen somebody is working in.
        .catch(() => setReport(null)),
    [],
  )

  // Once, on mount. Deliberately no polling: the SERVER holds the answer and
  // asks GitHub at most once a day, so a client timer would only re-read a
  // value that cannot have changed.
  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (!open) return
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('keydown', key)
    return () => document.removeEventListener('keydown', key)
  }, [open])

  const run = (p: Promise<UpdateReport>) => {
    setBusy(true)
    setError('')
    p.then(setReport)
      .catch((e: Error) => setError(e.message))
      .finally(() => setBusy(false))
  }

  if (!report) return null

  const admin = me.role === 'admin'
  const available = offer(report)
  const unread = available !== '' && !dismissed.includes(available)
  // The question, asked once, and only of somebody who can answer it. Putting
  // it to a member would be asking permission from a person the server will
  // refuse.
  const asking = report.mode === 'ask' && admin
  // Nothing to say and nothing to ask: no button at all. An always-present
  // control for a thing that is fine is chrome.
  if (!unread && !asking && !open) return null

  const dismiss = () => {
    const next = [...dismissed, available].slice(-20)
    setDismissed(next)
    try {
      localStorage.setItem(SEEN, JSON.stringify(next))
    } catch {
      // Nothing to do about it, and nothing to say: the cost is seeing this
      // again next time.
    }
    setOpen(false)
  }

  return (
    <>
      <button
        className={`rail-btn ${unread ? 'has-news' : ''}`}
        data-own
        onClick={() => setOpen((v) => !v)}
        title={unread ? 'A newer version is available' : 'Updates'}
      >
        <svg viewBox="0 0 24 24" aria-hidden>
          <path d="M12 20.2v-13M7.8 11.4 12 7.2l4.2 4.2" />
          <path d="M4.6 3.8h14.8" />
        </svg>
        <span className="sr-only">Updates</span>
        {unread && <span className="rail-badge news" aria-hidden />}
      </button>

      {open &&
        createPortal(
          <div className="modal-backdrop" onClick={() => setOpen(false)}>
            <div
              className="modal theme-dialog update-dialog"
              role="dialog"
              aria-modal="true"
              aria-label="Updates"
              onClick={(e) => e.stopPropagation()}
            >
              <div className="row theme-head">
                <h3>Updates</h3>
                <span className="spacer" />
                <button data-own className="drawer-x" onClick={() => setOpen(false)} title="Close">
                  ×
                </button>
              </div>

              {report.mode === 'off' && (
                <p className="hint">
                  Update checking is switched off in this server’s configuration, so nothing here has been asked
                  and nothing will be. That is a decision made on the server’s own disk — change{' '}
                  <code>update_check</code> there and restart.
                </p>
              )}

              {asking && (
                <div className="card update-ask">
                  <strong>May this install ask whether a newer version exists?</strong>
                  <p className="hint">
                    It is a plain GET to GitHub’s public releases API, once a day. <strong>Nothing about this
                    install is sent</strong> — no identifier, no version, no count, no usage. Nothing is
                    downloaded and nothing is ever replaced: what you get back is the release notes and the
                    command belonging to whatever installed this binary.
                  </p>
                  <p className="hint">
                    Say no and this product goes on fetching nothing at runtime, which is what it does today.
                  </p>
                  <div className="row">
                    <button className="primary" disabled={busy} onClick={() => run(api.updates.setMode('on'))}>
                      yes, check daily
                    </button>
                    <button disabled={busy} onClick={() => run(api.updates.setMode('off'))}>
                      no, never
                    </button>
                    <button disabled={busy} onClick={() => run(api.updates.checkNow())}>
                      just look once, now
                    </button>
                  </div>
                </div>
              )}

              {report.products.map((p) => (
                <Half key={p.name} p={p} />
              ))}

              {available !== '' && (
                <div className="card update-take">
                  <strong>Taking it</strong>
                  <p className="hint">{report.install.note}</p>
                  {report.install.command && (
                    <pre className="update-cmd">
                      <code>{report.install.command}</code>
                    </pre>
                  )}
                  <p className="hint">
                    Cogitorium never replaces its own binary. A self-updater that fights a package manager
                    produces a machine nobody can reason about — <code>brew list</code> saying one version, the
                    file being another, and the next upgrade quietly reverting it.
                  </p>
                </div>
              )}

              {error && <p className="error">{error}</p>}

              <div className="row update-foot">
                <span className="hint">
                  {report.checked_at && !report.checked_at.startsWith('0001')
                    ? `last asked ${new Date(report.checked_at).toLocaleString()}`
                    : 'never asked'}
                </span>
                <span className="spacer" />
                <span className="row">
                  {admin && report.mode !== 'off' && (
                    <button disabled={busy} onClick={() => run(api.updates.checkNow())}>
                      check now
                    </button>
                  )}
                  {admin && report.mode === 'on' && (
                    <button disabled={busy} onClick={() => run(api.updates.setMode('off'))}>
                      stop checking
                    </button>
                  )}
                  {unread && (
                    <button className="primary" onClick={dismiss}>
                      got it
                    </button>
                  )}
                </span>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}

/**
 * One product's half of the answer.
 *
 * Four states, kept apart on purpose. "Up to date" and "nothing here can say"
 * are different facts — a development build has no basis for claiming currency
 * — and "could not ask" is a third. Collapsing them would let this panel report
 * confidence it does not have.
 */
function Half({ p }: { p: UpdateProduct }) {
  if (!p.running) return null

  return (
    <div className={`card update-half ${p.newer ? 'newer' : ''}`}>
      <div className="row">
        <strong>{p.name}</strong>
        <span className="spacer" />
        <span className="muted">{p.running}</span>
      </div>

      {p.error && (
        <p className="hint">
          Could not ask: {p.error}. That is not the same as being up to date — nothing here knows either way.
        </p>
      )}

      {!p.error && !p.latest && <p className="hint">Nothing has been asked yet.</p>}

      {p.latest && !p.newer && p.comparable && <p className="hint">This is the newest release.</p>}

      {p.latest && !p.comparable && (
        <p className="hint">
          The newest release is <strong>{p.latest.tag}</strong>. This build’s version could not be read as a
          release version, so nothing here can tell you whether that is newer — which is usually because it was
          built from source rather than installed.
        </p>
      )}

      {p.newer && p.latest && (
        <>
          <p>
            <strong>{p.latest.tag}</strong> is out
            {p.latest.published_at ? ` — ${new Date(p.latest.published_at).toLocaleDateString()}` : ''}.
          </p>
          {/* The notes as text, never as rendered markup: this is somebody
              else's document arriving over the network, and a release body
              that rendered as HTML in this interface would be a hole. */}
          {p.latest.notes && <pre className="update-notes">{p.latest.notes}</pre>}
          {p.latest.url && (
            <a href={p.latest.url} target="_blank" rel="noreferrer">
              the release page
            </a>
          )}
        </>
      )}
    </div>
  )
}
