import { useEffect, useRef, useState } from 'react'
import { api, chatStream, type ChatMessage, type Model } from '../api'

export default function ChatPage() {
  const [models, setModels] = useState<Model[]>([])
  const [modelId, setModelId] = useState<number | null>(null)
  const [system, setSystem] = useState('')
  const [transcript, setTranscript] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const bottom = useRef<HTMLDivElement>(null)

  useEffect(() => {
    api.models
      .list()
      .then((m) => {
        setModels(m)
        if (m.length > 0) setModelId((cur) => cur ?? m[0].id)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  useEffect(() => bottom.current?.scrollIntoView({ behavior: 'smooth' }), [transcript])

  const send = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!modelId || !input.trim() || busy) return
    setError(null)
    setBusy(true)

    const userMsg: ChatMessage = { role: 'user', content: input.trim() }
    const history = [...transcript, userMsg]
    setTranscript([...history, { role: 'assistant', content: '' }])
    setInput('')

    const wire: ChatMessage[] = system.trim() ? [{ role: 'system', content: system.trim() }, ...history] : history

    try {
      await chatStream(modelId, wire, (text) =>
        setTranscript((t) => {
          const next = [...t]
          const last = next[next.length - 1]
          next[next.length - 1] = { ...last, content: last.content + text }
          return next
        }),
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      // Drop the empty assistant stub if nothing streamed.
      setTranscript((t) => (t[t.length - 1]?.content === '' ? t.slice(0, -1) : t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="page chat-page">
      <h2>Scratch chat</h2>
      <p className="hint">A direct line to one catalog model — for checking that a model is wired up correctly.</p>
      <div className="row">
        <select value={modelId ?? ''} onChange={(e) => setModelId(Number(e.target.value))}>
          {models.length === 0 && <option value="">no models in catalog</option>}
          {models.map((m) => (
            <option key={m.id} value={m.id}>
              {m.provider_name} / {m.label || m.model_name}
            </option>
          ))}
        </select>
        <input
          className="grow"
          placeholder="system prompt (optional)"
          value={system}
          onChange={(e) => setSystem(e.target.value)}
        />
        <button onClick={() => setTranscript([])} disabled={transcript.length === 0}>
          clear
        </button>
      </div>
      <div className="transcript">
        {transcript.map((m, i) => (
          <div key={i} className={`msg ${m.role}`}>
            <span className="msg-role">{m.role}</span>
            <div className="msg-body">{m.content || (busy && i === transcript.length - 1 ? '…' : '')}</div>
          </div>
        ))}
        <div ref={bottom} />
      </div>
      {error && <p className="error">{error}</p>}
      <form className="row" onSubmit={send}>
        <input
          className="grow"
          placeholder={modelId ? 'message…' : 'add a model to the catalog first'}
          value={input}
          disabled={!modelId || busy}
          onChange={(e) => setInput(e.target.value)}
        />
        <button type="submit" disabled={!modelId || busy || !input.trim()}>
          {busy ? 'streaming…' : 'send'}
        </button>
      </form>
    </div>
  )
}
