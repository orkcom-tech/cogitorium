import { useCallback, useEffect, useState } from 'react'
import { api, type ContextFile, type ContextSearch, type ContextStatus } from '../api'
import { PanelTitle } from '../deck/Drawer'

export default function ContextPage() {
  const [status, setStatus] = useState<ContextStatus | null>(null)
  const [files, setFiles] = useState<ContextFile[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const [original, setOriginal] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // The version this editor opened. Handed back on save so a save can be
  // refused rather than quietly overwriting somebody else's work.
  const [opened, setOpened] = useState('')
  const [query, setQuery] = useState('')
  const [hits, setHits] = useState<ContextSearch | null>(null)
  const [searching, setSearching] = useState(false)

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
        setOpened(f.version)
        setError(null)
        setNotice(null)
      })
      .catch((e: Error) => setError(e.message))
  }

  const save = () => {
    if (!selected) return
    api.context
      .put(selected, content, opened)
      .then(() => {
        setOriginal(content)
        setNotice(`saved — contextd created a new version of ${selected}`)
        setError(null)
        // Reopen for the new version, or the next save would hand back a
        // version that is one behind and be refused for no reason.
        void api.context.get(selected).then((f) => setOpened(f.version))
        reload()
      })
      // A refusal here is the point of the guard, and it arrives as the
      // server's own sentence — which version it is at and which one was
      // opened — rather than "save failed".
      .catch((e: Error) => setError(e.message))
  }

  const search = () => {
    if (!query.trim()) {
      setHits(null)
      return
    }
    setSearching(true)
    api.context
      .search(query)
      .then((r) => {
        setHits(r)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))
      .finally(() => setSearching(false))
  }

  if (status && !status.available) {
    return (
      <div className="page">
        <PanelTitle>Context</PanelTitle>
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
      <PanelTitle>Context</PanelTitle>
      <p className="hint">
        Files in your Contextverse space{status?.space_root ? ` (${status.space_root})` : ''}. Editing here writes
        through <code>contextd</code>, so versioning stays Contextverse's.
      </p>
      {error && <p className="error">{error}</p>}
      {notice && <p className="hint">✓ {notice}</p>}
      {/* Finding a memory used to mean already knowing its path. */}
      <form
        className="row"
        onSubmit={(e) => {
          e.preventDefault()
          search()
        }}
      >
        <input
          className="grow"
          placeholder="search inside the files…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button type="submit" disabled={searching || !query.trim()}>
          {searching ? 'searching…' : 'search'}
        </button>
        {hits && (
          <button
            type="button"
            onClick={() => {
              setHits(null)
              setQuery('')
            }}
          >
            clear
          </button>
        )}
      </form>

      {hits && (
        <div className="card">
          <p className="hint">
            {hits.matches.length === 0
              ? `Nothing in ${hits.files_scanned} files mentions “${hits.query}”.`
              : `${hits.matches.length} line${hits.matches.length === 1 ? '' : 's'} in ${hits.files_matched} of ${hits.files_scanned} files.`}
            {/* A cut answer that does not say it was cut reads as "there is
                nothing else", which is the one wrong answer a search gives. */}
            {hits.truncated ? ' There are more — narrow the search.' : ''}
          </p>
          {hits.matches.map((m, i) => (
            <button key={`${m.path}:${m.line}:${i}`} className="context-hit" onClick={() => open(m.path)}>
              <code className="path">
                {m.path}:{m.line}
              </code>
              <span className="hit-text">{m.text.trim()}</span>
            </button>
          ))}
        </div>
      )}

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
                {opened && <span className="muted">opened at {opened}</span>}
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
