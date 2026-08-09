import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { langOf, tokenize } from './highlight'

/**
 * The file editor, as its own panel.
 *
 * It used to be the right-hand half of the Files panel, which meant the tree's
 * width and the editor's width were one number: in a 365px panel the editor was
 * about fifty pixels and every word broke to one character per line. A tree is
 * a narrow thing and a file is a wide one; they are two panels.
 *
 * Highlighting is a <pre> painted underneath a transparent <textarea>. That is
 * the one approach that keeps a real textarea — real caret, real selection,
 * real IME, real undo, real spellcheck-off — instead of reimplementing text
 * editing on a contenteditable. The two layers must agree on font, size, line
 * height, padding, whitespace handling and scroll offset to the pixel, which is
 * why those live in one CSS block and not two.
 */
export default function CodeEditor({
  wsId,
  path,
  onClose,
  onSaved,
  onError,
}: {
  wsId: number
  path: string | null
  onClose: () => void
  onSaved: (path: string) => void
  onError: (m: string) => void
}) {
  const [text, setText] = useState('')
  const [saved, setSaved] = useState('')
  const [loading, setLoading] = useState(false)
  const [note, setNote] = useState<string | null>(null)
  const [wrap, setWrap] = useState(false)
  const area = useRef<HTMLTextAreaElement>(null)
  const back = useRef<HTMLPreElement>(null)

  useEffect(() => {
    if (!path) return
    setLoading(true)
    setNote(null)
    let live = true
    api.files
      .read(wsId, path)
      .then((f) => {
        // A slow read for a file the operator has already navigated away from
        // must not overwrite the one they are looking at now.
        if (!live) return
        setText(f.content)
        setSaved(f.content)
      })
      .catch((e: Error) => {
        // Binary and oversized files are refused by the server on purpose, and
        // that reason belongs in the panel rather than in a banner somewhere
        // else on the screen.
        if (live) setNote(e.message)
      })
      .finally(() => {
        if (live) setLoading(false)
      })
    return () => {
      live = false
    }
  }, [wsId, path, onError])

  const dirty = text !== saved

  const save = useCallback(() => {
    if (!path || !dirty) return
    api.files
      .write(wsId, path, text)
      .then(() => {
        setSaved(text)
        setNote('saved')
        onSaved(path)
      })
      .catch((e: Error) => onError(e.message))
  }, [wsId, path, text, dirty, onSaved, onError])

  // Tokenizing on every keystroke is fine for the sizes this panel opens —
  // it is a linear scan — but memoizing keeps it off the typing path when the
  // change was a selection or a scroll rather than an edit.
  const lang = path ? langOf(path) : ''
  const tokens = useMemo(() => tokenize(text, lang), [text, lang])

  // The layers scroll as one. Without this the highlight sits still while the
  // text moves, which is worse than no highlighting at all.
  const sync = () => {
    if (!area.current || !back.current) return
    back.current.scrollTop = area.current.scrollTop
    back.current.scrollLeft = area.current.scrollLeft
  }

  if (!path) {
    return (
      <div className="bn-body editor-empty">
        <p className="hint">Pick a file in the tree to open it here.</p>
      </div>
    )
  }

  const lines = text.length ? text.split('\n').length : 1

  return (
    <div className="bn-body code-editor">
      <div className="row editor-head">
        <strong className="editor-path" title={path}>
          {path}
        </strong>
        {dirty && <span className="warn">unsaved</span>}
        {!dirty && note && <span className="muted">{note}</span>}
        <span className="spacer" />
        <span className="muted editor-stat">
          {lines} {lines === 1 ? 'line' : 'lines'}
          {lang ? ` · ${lang}` : ''}
        </span>
        <button onClick={() => setWrap((w) => !w)} title="Wrap long lines">
          {wrap ? 'no wrap' : 'wrap'}
        </button>
        <button
          onClick={() => {
            setText(saved)
            setNote(null)
          }}
          disabled={!dirty}
        >
          revert
        </button>
        <button className="primary" onClick={save} disabled={!dirty}>
          save
        </button>
        <button onClick={onClose} title="Close the file">
          ×
        </button>
      </div>

      {loading ? (
        <p className="hint">opening…</p>
      ) : (
        <div className={`code-stack ${wrap ? 'wrap' : ''}`}>
          <pre className="code-back" ref={back} aria-hidden>
            {tokens.map((t, i) => (
              <span key={i} className={`tk-${t.kind}`}>
                {t.text}
              </span>
            ))}
            {/* A trailing newline is not rendered by <pre>, so without this the
                last line scrolls out from under the textarea's own last line. */}
            {'\n'}
          </pre>
          <textarea
            ref={area}
            className="code-front"
            value={text}
            spellCheck={false}
            autoCorrect="off"
            autoCapitalize="off"
            aria-label={path}
            onScroll={sync}
            onChange={(e) => {
              setText(e.target.value)
              setNote(null)
            }}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === 's') {
                e.preventDefault()
                save()
                return
              }
              // Tab indents rather than leaving the field. In a panel whose
              // whole job is editing code, moving focus is never what Tab was
              // meant to do — and Escape still gets you out for accessibility.
              if (e.key === 'Tab') {
                e.preventDefault()
                const el = e.currentTarget
                const { selectionStart: s, selectionEnd: en } = el
                const next = text.slice(0, s) + '  ' + text.slice(en)
                setText(next)
                requestAnimationFrame(() => el.setSelectionRange(s + 2, s + 2))
              }
            }}
          />
        </div>
      )}
    </div>
  )
}
