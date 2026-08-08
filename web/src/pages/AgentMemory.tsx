import { useCallback, useEffect, useState } from 'react'
import { api, type Agent, type MemoryItem } from '../api'

const LABELS: Record<MemoryItem['kind'], string> = {
  role: 'role',
  private: 'private to this agent',
  shared: 'shared with the workspace',
  bound: 'bound document',
  instruction: 'from the library',
}

// Everything this agent remembers, in the order it reaches the model, each
// piece traceable to where it came from and changeable on the spot.
//
// The point is not tidiness. An agent that quietly carries something it
// picked up once will keep steering by it, and the only way to stop that is
// to be able to see it. So nothing here is hidden behind a summary: the
// text is the text, and next to it is the way to change or drop it.
export default function AgentMemory({
  agent,
  onChanged,
  onError,
}: {
  agent: Agent
  onChanged: () => void
  onError: (m: string) => void
}) {
  const [items, setItems] = useState<MemoryItem[] | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [draft, setDraft] = useState('')

  const reload = useCallback(() => {
    api.agents
      .memory(agent.id)
      .then(setItems)
      .catch((e: Error) => onError(e.message))
  }, [agent.id, onError])

  useEffect(reload, [reload])

  const save = (item: MemoryItem) => {
    const done = () => {
      setEditing(null)
      reload()
      onChanged()
    }
    if (item.kind === 'role') {
      api.agents
        .update(agent.id, { role: draft })
        .then(done)
        .catch((e: Error) => onError(e.message))
      return
    }
    api.context
      .put(item.source, draft)
      .then(done)
      .catch((e: Error) => onError(e.message))
  }

  const forget = (item: MemoryItem) => {
    if (item.binding_id != null) {
      if (!confirm(`Stop this agent reading ${item.source}? The document itself stays.`)) return
      api.context
        .unbind(item.binding_id)
        .then(() => {
          reload()
          onChanged()
        })
        .catch((e: Error) => onError(e.message))
      return
    }
    // Contextverse's CLI has no delete, so emptying is how a memory is
    // dropped — and an empty document is skipped when the prompt is built,
    // so the effect is the same as forgetting it.
    if (!confirm(`Clear ${item.source}? Its previous versions stay in Contextverse.`)) return
    api.context
      .put(item.source, '')
      .then(() => {
        reload()
        onChanged()
      })
      .catch((e: Error) => onError(e.message))
  }

  if (!items) return <p className="hint">reading memory…</p>

  return (
    <>
      <p className="hint">
        Everything below reaches the model on every turn, in this order. If an agent behaves in a way you did not
        ask for, the reason is here — change it or drop it.
      </p>
      {items.map((item) => (
        <div key={item.source} className="card memory-item">
          <div className="card-head">
            <span className={`status ${item.kind === 'role' ? 'approved' : 'pending'}`}>{LABELS[item.kind]}</span>
            <code>{item.source}</code>
            <span className="muted">{item.description}</span>
            <span className="spacer" />
            {item.editable && editing !== item.source && (
              <button
                onClick={() => {
                  setEditing(item.source)
                  setDraft(item.content)
                }}
              >
                edit
              </button>
            )}
            {editing === item.source && (
              <>
                <button onClick={() => save(item)}>save</button>
                <button onClick={() => setEditing(null)}>cancel</button>
              </>
            )}
            {item.kind !== 'role' && (
              <button className="danger" onClick={() => forget(item)}>
                {item.removable ? 'unbind' : 'forget'}
              </button>
            )}
          </div>
          {editing === item.source ? (
            <textarea rows={10} value={draft} onChange={(e) => setDraft(e.target.value)} />
          ) : (
            <pre className="prompt-preview">{item.content || '(empty — nothing reaches the model from this)'}</pre>
          )}
        </div>
      ))}
      <p className="hint">
        The conversation is memory too: it is replayed on every turn. Anything wrong in it can be dropped from the
        chat, entry by entry.
      </p>
    </>
  )
}
