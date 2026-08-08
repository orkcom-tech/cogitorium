import { useCallback, useEffect, useState } from 'react'
import { api, type Model, type Provider, type TestResult } from '../api'

export default function ModelsPage() {
  const [providers, setProviders] = useState<Provider[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    Promise.all([api.providers.list(), api.models.list()])
      .then(([p, m]) => {
        setProviders(p)
        setModels(m)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  useEffect(reload, [reload])

  return (
    <div className="page">
      <h2>Model catalog</h2>
      {error && <p className="error">{error}</p>}
      <section>
        <h3>Providers</h3>
        <AddProviderForm onDone={reload} />
        {providers.length === 0 && <p className="hint">No providers yet — add one above.</p>}
        {providers.map((p) => (
          <ProviderCard key={p.id} provider={p} models={models} onChange={reload} />
        ))}
      </section>
      <section>
        <h3>Catalog</h3>
        {models.length === 0 ? (
          <p className="hint">Empty. Add models from a provider card above.</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Model</th>
                <th>Label</th>
                <th>Provider</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {models.map((m) => (
                <tr key={m.id}>
                  <td>
                    <code>{m.model_name}</code>
                  </td>
                  <td>{m.label}</td>
                  <td>
                    {m.provider_name} <span className="muted">({m.provider_type})</span>
                  </td>
                  <td>
                    <button
                      className="danger"
                      onClick={() => api.models.remove(m.id).then(reload).catch((e: Error) => setError(e.message))}
                    >
                      remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  )
}

function AddProviderForm({ onDone }: { onDone: () => void }) {
  const [name, setName] = useState('')
  const [type, setType] = useState('anthropic')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState<string | null>(null)

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    api.providers
      .create({ name, type, base_url: baseURL, api_key: apiKey })
      .then(() => {
        setName('')
        setBaseURL('')
        setApiKey('')
        setError(null)
        onDone()
      })
      .catch((e: Error) => setError(e.message))
  }

  return (
    <form className="row" onSubmit={submit}>
      <input required placeholder="name (e.g. anthropic, ollama)" value={name} onChange={(e) => setName(e.target.value)} />
      <select value={type} onChange={(e) => setType(e.target.value)}>
        <option value="anthropic">anthropic</option>
        <option value="openai-compatible">openai-compatible</option>
      </select>
      <input
        placeholder={type === 'anthropic' ? 'base URL (default: api.anthropic.com)' : 'base URL (e.g. http://localhost:11434/v1)'}
        value={baseURL}
        onChange={(e) => setBaseURL(e.target.value)}
      />
      <input placeholder="API key (optional for local)" type="password" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
      <button type="submit">add provider</button>
      {error && <span className="error">{error}</span>}
    </form>
  )
}

function ProviderCard({ provider, models, onChange }: { provider: Provider; models: Model[]; onChange: () => void }) {
  const [test, setTest] = useState<TestResult | null>(null)
  const [testing, setTesting] = useState(false)
  const [manualName, setManualName] = useState('')
  const [error, setError] = useState<string | null>(null)

  const inCatalog = new Set(models.filter((m) => m.provider_id === provider.id).map((m) => m.model_name))

  const runTest = () => {
    setTesting(true)
    api.providers
      .test(provider.id)
      .then(setTest)
      .catch((e: Error) => setTest({ ok: false, error: e.message }))
      .finally(() => setTesting(false))
  }

  const addModel = (modelName: string) => {
    api.models
      .create({ provider_id: provider.id, model_name: modelName, label: '' })
      .then(() => {
        setError(null)
        onChange()
      })
      .catch((e: Error) => setError(e.message))
  }

  return (
    <div className="card">
      <div className="card-head">
        <strong>{provider.name}</strong>
        <span className="muted">
          {provider.type} · {provider.base_url} · {provider.has_key ? 'key set' : 'no key'}
        </span>
        <span className="spacer" />
        <button onClick={runTest} disabled={testing}>
          {testing ? 'testing…' : 'test / list models'}
        </button>
        <button
          className="danger"
          onClick={() => {
            if (confirm(`Delete provider "${provider.name}" and its catalog models?`))
              api.providers.remove(provider.id).then(onChange).catch((e: Error) => setError(e.message))
          }}
        >
          delete
        </button>
      </div>
      {test && !test.ok && <p className="error">test failed: {test.error}</p>}
      {test?.ok && (
        <div className="pick-list">
          {(test.models ?? []).map((m) => (
            <button key={m} disabled={inCatalog.has(m)} onClick={() => addModel(m)} title="add to catalog">
              {inCatalog.has(m) ? `✓ ${m}` : `+ ${m}`}
            </button>
          ))}
          {(test.models ?? []).length === 0 && <span className="hint">provider reports no models</span>}
        </div>
      )}
      <form
        className="row"
        onSubmit={(e) => {
          e.preventDefault()
          if (manualName.trim()) {
            addModel(manualName.trim())
            setManualName('')
          }
        }}
      >
        <input placeholder="or add model name manually" value={manualName} onChange={(e) => setManualName(e.target.value)} />
        <button type="submit">add</button>
        {error && <span className="error">{error}</span>}
      </form>
    </div>
  )
}
