import { useCallback, useEffect, useRef, useState } from 'react'
import { Select } from './Select'
import { Field, Fields } from './Field'
import { api, type Instruction } from '../api'
import { dragging } from '../dnd'
import { PanelTitle } from '../deck/Drawer'

// The instruction library: guidance written once and reused, so nobody
// retypes house style into every agent's role. It mirrors the gear
// catalogue on purpose — the same searching, tagging and provenance — and
// differs where it should: nothing here executes, so nothing needs
// approving, and the text itself lives in Contextverse.
export default function LibraryPage() {
  const [items, setItems] = useState<Instruction[]>([])
  const [query, setQuery] = useState('')
  const [tag, setTag] = useState('')
  const [openId, setOpenId] = useState<number | null>(null)
  const [text, setText] = useState('')
  const [writing, setWriting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seqRef = useRef(0)

  const reload = useCallback(() => {
    const seq = ++seqRef.current
    api.instructions
      .list(query, tag)
      .then((r) => {
        if (seqRef.current === seq) setItems(r)
      })
      .catch((e: Error) => {
        if (seqRef.current === seq) setError(e.message)
      })
  }, [query, tag])

  useEffect(reload, [reload])

  const open = (i: Instruction) => {
    if (openId === i.id) {
      setOpenId(null)
      return
    }
    api.instructions
      .get(i.id)
      .then((r) => {
        setOpenId(i.id)
        setText(r.text)
      })
      .catch((e: Error) => setError(e.message))
  }

  const allTags = [...new Set(items.flatMap((i) => i.tags))].sort()

  return (
    <div className="page">
      <PanelTitle>Instructions</PanelTitle>
      <p className="hint">
        Guidance worth keeping: house style, procedures, checklists. Bind one to a workspace or a single agent from
        the agent panel. The text lives in Contextverse, which versions it — this is the catalogue that makes it
        findable.
      </p>
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
        <button onClick={() => setWriting((v) => !v)}>{writing ? 'cancel' : 'write one'}</button>
      </div>

      {writing && (
        <WriteInstructionForm
          onDone={() => {
            setWriting(false)
            reload()
          }}
          onError={setError}
        />
      )}

      {items.length === 0 ? (
        <p className="hint">
          Nothing here yet. Agents save what they work out, and you can write one yourself.
        </p>
      ) : (
        items.map((i) => (
          <div
            key={i.id}
            className="card gear-card-drag"
            // Same rule as a gear: collapsed it is a thing you can pick up,
            // open it is a wall of text you are reading.
            draggable={openId !== i.id}
            onDragStart={dragging({ kind: 'instruction', id: i.id, name: i.name, path: i.path })}
            title={openId === i.id ? undefined : 'Drag onto the blueprint to give this to an agent'}
          >
            <div className="card-head">
              <strong>{i.name}</strong>
              <span className="muted">
                {i.created_by_agent ? `saved by ${i.created_by_agent}` : 'written by you'}
                {i.origin_workspace ? ` in ${i.origin_workspace}` : ''}
              </span>
              <span className="spacer" />
              <button onClick={() => open(i)}>{openId === i.id ? 'hide' : 'read'}</button>
              <button
                className="danger"
                onClick={() => {
                  if (confirm(`Remove "${i.name}" from the catalogue? Its text stays in Contextverse.`))
                    api.instructions.remove(i.id).then(reload).catch((e: Error) => setError(e.message))
                }}
              >
                unlist
              </button>
            </div>
            <p className="gear-desc">{i.description}</p>
            {i.tags.length > 0 && (
              <div className="pick-list">
                {i.tags.map((t) => (
                  <code key={t} className="chip">
                    {t}
                  </code>
                ))}
              </div>
            )}
            {openId === i.id && (
              <>
                <pre className="prompt-preview">{text}</pre>
                <p className="hint">
                  <code>{i.path}</code> in Contextverse
                </p>
              </>
            )}
          </div>
        ))
      )}
    </div>
  )
}

function WriteInstructionForm({ onDone, onError }: { onDone: () => void; onError: (m: string) => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [tags, setTags] = useState('')
  const [text, setText] = useState('')

  return (
    <form
      className="card"
      onSubmit={(e) => {
        e.preventDefault()
        api.instructions
          .save({
            name,
            description,
            text,
            tags: tags.split(',').map((t) => t.trim()).filter(Boolean),
          })
          .then(onDone)
          .catch((err: Error) => onError(err.message))
      }}
    >
      <Fields>
        <Field label="Name" hint="lower case with dashes">
          <input required placeholder="review-checklist" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="What it is for" wide hint="an agent reads this when deciding whether to pin it, so write it for them">
          <input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field label="Tags" hint="comma separated, and used to filter this list">
          <input placeholder="review, python" value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </Fields>
      <Field label="The instruction" wide hint="markdown; this is the text pinned onto an agent verbatim">
        <textarea rows={10} value={text} onChange={(e) => setText(e.target.value)} />
      </Field>
      <div className="row form-actions">
        <button type="submit" className="primary" disabled={!name.trim() || !text.trim()}>
          save to the library
        </button>
      </div>
    </form>
  )
}
