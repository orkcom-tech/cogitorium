import { useEffect, useRef, useState } from 'react'
import { api, chatStream, type ChatMessage, type Model } from '../api'

export default function ChatPage() {
  const [models, setModels] = useState<Model[]>([])
  const [modelId, setModelId] = useState<number | null>(null)
  const [system, setSystem] = useState('')
  const [transcript, setTranscript] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const bottom = useRef<HTMLDivElement>(null)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    api.models
      .list()
      .then((m) => {
        setModels(m)
        if (m.length > 0) setModelId((cur) => cur ?? m[0].id)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  // Abort any in-flight stream when the page unmounts.
  useEffect(() => () => abortRef.current?.abort(), [])

  // Scroll the transcript itself, never scrollIntoView: that walks UP the tree
  // and scrolls every scrollable ancestor, which drags the workspace header
  // off screen. Only follow when the operator is already at the bottom —
  // yanking them away from something they scrolled back to read is worse than
  // missing the newest line.
  useEffect(() => {
    const el = bottom.current?.parentElement
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 120
    if (nearBottom) el.scrollTop = el.scrollHeight
  }, [transcript])

  const send = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!modelId || !input.trim() || busy) return
    setError(null)
    setNotice(null)
    setBusy(true)

    const userMsg: ChatMessage = { role: 'user', content: input.trim() }
    const history = [...transcript, userMsg].filter((m) => m.content.trim() !== '')
    setTranscript([...history, { role: 'assistant', content: '' }])
    setInput('')

    const wire: ChatMessage[] = system.trim() ? [{ role: 'system', content: system.trim() }, ...history] : history

    const ac = new AbortController()
    abortRef.current = ac
    try {
      const res = await chatStream(
        modelId,
        wire,
        (text) =>
          setTranscript((t) => {
            const last = t[t.length - 1]
            if (!last || last.role !== 'assistant') return t
            return [...t.slice(0, -1), { ...last, content: last.content + text }]
          }),
        ac.signal,
      )
      if (res.truncated) {
        setNotice(`response was cut off by the token limit (${res.stopReason})`)
      }
    } catch (err) {
      if (!ac.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      abortRef.current = null
      setBusy(false)
      // Drop the assistant stub if nothing streamed into it.
      setTranscript((t) => {
        const last = t[t.length - 1]
        return last && last.role === 'assistant' && last.content === '' ? t.slice(0, -1) : t
      })
    }
  }

  const stop = () => abortRef.current?.abort()

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
        <button onClick={() => setTranscript([])} disabled={busy || transcript.length === 0}>
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
      {notice && <p className="hint">⚠ {notice}</p>}
      {error && <p className="error">{error}</p>}
      <form className="row" onSubmit={send}>
        <input
          className="grow"
          placeholder={modelId ? 'message…' : 'add a model to the catalog first'}
          value={input}
          disabled={!modelId || busy}
          onChange={(e) => setInput(e.target.value)}
        />
        {busy ? (
          <button type="button" onClick={stop}>
            stop
          </button>
        ) : (
          <button type="submit" disabled={!modelId || !input.trim()}>
            send
          </button>
        )}
      </form>
    </div>
  )
}
