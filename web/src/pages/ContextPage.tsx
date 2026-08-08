import { useCallback, useEffect, useState } from 'react'
import { api, type ContextFile, type ContextStatus } from '../api'

export default function ContextPage() {
  const [status, setStatus] = useState<ContextStatus | null>(null)
  const [files, setFiles] = useState<ContextFile[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const [original, setOriginal] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const reload = useCallback(() => {
    api.context
      .status()
      .then((st) => {
        setStatus(st)
        if (!st.available) return
        return api.context.files().then(setFiles)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  useEffect(reload, [reload])

  const open = (path: string) => {
    if (selected && content !== original && !confirm(`Discard unsaved changes to ${selected}?`)) return
    api.context
      .get(path)
      .then((f) => {
        setSelected(path)
        setContent(f.content)
        setOriginal(f.content)
        setError(null)
        setNotice(null)
      })
      .catch((e: Error) => setError(e.message))
  }

  const save = () => {
    if (!selected) return
    api.context
      .put(selected, content)
      .then(() => {
        setOriginal(content)
        setNotice(`saved — contextd created a new version of ${selected}`)
        setError(null)
        reload()
      })
      .catch((e: Error) => setError(e.message))
  }

  if (status && !status.available) {
    return (
      <div className="page">
        <h2>Context</h2>
        <div className="card">
          <p>
            Context lives in <strong>Contextverse</strong> — Cogitorium does not store it. The <code>contextd</code>{' '}
            CLI is not usable right now:
          </p>
          <p className="error">{status.error}</p>
          <p className="hint">
            Install Contextverse and run <code>contextd init solo</code>, or point Cogitorium at the binary with{' '}
            <code>contextd_path</code> in config.yaml (or <code>COGITORIUM_CONTEXTD</code>).
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="page">
      <h2>Context</h2>
      <p className="hint">
        Files in your Contextverse space{status?.space_root ? ` (${status.space_root})` : ''}. Editing here writes
        through <code>contextd</code>, so versioning stays Contextverse's.
      </p>
      {error && <p className="error">{error}</p>}
      {notice && <p className="hint">✓ {notice}</p>}
      <div className="context-layout">
        <div className="context-files">
          {files.length === 0 && <p className="hint">space is empty</p>}
          {files.map((f) => (
            <button
              key={f.path}
              className={`context-file ${selected === f.path ? 'selected' : ''}`}
              onClick={() => open(f.path)}
            >
              <span className="path">{f.path}</span>
              <span className="muted">{f.version}</span>
            </button>
          ))}
        </div>
        <div className="context-editor">
          {selected ? (
            <>
              <div className="row">
                <code>{selected}</code>
                <span className="spacer" />
                <button disabled={content === original} onClick={save}>
                  save new version
                </button>
              </div>
              <textarea value={content} onChange={(e) => setContent(e.target.value)} spellCheck={false} />
            </>
          ) : (
            <p className="hint">Pick a file to view or edit it.</p>
          )}
        </div>
      </div>
    </div>
  )
}
