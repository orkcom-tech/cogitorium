import { useCallback, useEffect, useState } from 'react'
import { Field, Fields } from './Field'
import { Select } from './Select'
import { api, type EnvRecord, type EnvSources } from '../api'

/**
 * The named values a gear is given at run time — one component for both
 * scopes, because they are one mechanism and a second copy would drift.
 *
 * `wsId` undefined is the install-wide scope (administrators only, server-side:
 * one name here reaches every workspace). A number is that workspace's own
 * overrides, behind the same access rule as everything else in it.
 *
 * The rule this screen exists to make visible: a gear is given NAMES, and reads
 * the VALUES from its own environment. A model never sees a value. So a secret
 * is typed here once and never shown again — not hidden behind a reveal button,
 * not fetched on demand: the server has nowhere to put it in a response.
 */
export default function EnvPanel({
  wsId,
  // shown false means the panel is mounted but not placed on the bench, so it
  // does not spend a query on a screen nobody asked for. Undefined is a page,
  // which is always shown by virtue of having been navigated to.
  shown = true,
  onError,
}: {
  wsId?: number
  shown?: boolean
  onError?: (m: string) => void
}) {
  const [values, setValues] = useState<EnvRecord[]>([])
  const [sources, setSources] = useState<EnvSources | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [justSet, setJustSet] = useState<string | null>(null)

  const fail = useCallback(
    (e: Error) => {
      setError(e.message)
      onError?.(e.message)
    },
    [onError],
  )

  const reload = useCallback(() => {
    const p = wsId == null ? api.env.list() : api.env.listWorkspace(wsId)
    p.then((r) => {
      setValues(r.values)
      setSources(r.sources)
      setError(null)
    }).catch(fail)
  }, [wsId, fail])

  useEffect(() => {
    if (shown) reload()
  }, [shown, reload])

  const remove = (name: string) => {
    const warning =
      wsId == null
        ? `Delete "${name}"? Every gear that asks for it stops running until something else supplies it.`
        : `Delete this workspace's "${name}"? Gears here start seeing the install-wide value instead, if there is one.`
    if (!confirm(warning)) return
    const p = wsId == null ? api.env.remove(name) : api.env.removeWorkspace(wsId, name)
    p.then(reload).catch(fail)
  }

  return (
    <div className="env-panel">
      <p className="hint">
        Names a gear is given when it runs. A gear asks for <code>API_KEY</code>; you say what{' '}
        <code>API_KEY</code> means. The value goes into the gear's environment and nowhere else — no agent sees it, no
        model is told it, and anything a gear prints has it removed.
      </p>
      {wsId != null && (
        <p className="hint">
          These are this workspace's own. A name set here wins over the same name set install-wide, which is how one
          gear serves staging and production without being edited.
        </p>
      )}
      {sources && !sources.can_store_secrets && (
        <p className="notice">
          This server has no <code>COGITORIUM_SECRET_KEY</code>, so a secret cannot be stored in its database — it
          would have to be written to disk in plaintext, and it will not be. Variables work; set the key, or mount a
          secrets directory.
        </p>
      )}
      {sources && (sources.variables_dir || sources.secrets_dir) && (
        <p className="hint">
          Names are also read from disk, one file per name:{' '}
          {sources.variables_dir && (
            <>
              variables from <code>{sources.variables_dir}</code>
            </>
          )}
          {sources.variables_dir && sources.secrets_dir && ', '}
          {sources.secrets_dir && (
            <>
              secrets from <code>{sources.secrets_dir}</code>
            </>
          )}
          . A file there wins over what is set here install-wide, and a workspace's own value wins over both.
        </p>
      )}
      {error && <p className="error">{error}</p>}
      {justSet && (
        <p className="notice">
          <strong>{justSet}</strong> is set. If it is a secret, this was the only time it will be shown anywhere — the
          server keeps it sealed and has nowhere to put it in a response.
        </p>
      )}

      <SetEnvForm
        canStoreSecrets={sources?.can_store_secrets ?? false}
        onSave={(v) => {
          const p =
            wsId == null
              ? api.env.set(v.name, v)
              : api.env.setWorkspace(wsId, v.name, v)
          return p
            .then(() => {
              setJustSet(v.name)
              reload()
            })
            .catch(fail)
        }}
      />

      {values.length === 0 ? (
        <p className="hint">
          Nothing set {wsId == null ? 'for this install' : 'in this workspace'} yet.
        </p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>name</th>
              <th>kind</th>
              <th>value</th>
              <th>note</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {values.map((v) => (
              <tr key={v.id}>
                <td>
                  <code>{v.name}</code>
                </td>
                <td className={v.kind === 'secret' ? 'env-secret' : 'muted'}>{v.kind}</td>
                <td className="muted">
                  {/* A secret's value is not here because it is not in the
                      response. Saying that plainly is better than an empty cell,
                      which reads as "not set". */}
                  {v.kind === 'secret' ? <em>set, and never shown again</em> : <code>{v.value}</code>}
                </td>
                <td className="muted">{v.description}</td>
                <td>
                  <button className="danger" onClick={() => remove(v.name)}>
                    delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

/**
 * Setting one name. Saving an existing name replaces it, which is how a key is
 * rotated — so the form says so rather than pretending every save is a new
 * entry.
 */
function SetEnvForm({
  canStoreSecrets,
  onSave,
}: {
  canStoreSecrets: boolean
  onSave: (v: { name: string; kind: string; value: string; description: string }) => Promise<unknown>
}) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState<'variable' | 'secret'>('variable')
  const [value, setValue] = useState('')
  const [description, setDescription] = useState('')
  const [saving, setSaving] = useState(false)

  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        setSaving(true)
        // Upper-cased here rather than by the server: the same name has to
        // match a file in a mounted directory byte for byte, so the server
        // refuses a lowercase one instead of quietly changing it. Doing it in
        // the field is help; doing it in the store would be a trap.
        void onSave({ name: name.trim().toUpperCase(), kind, value, description })
          .then(() => {
            setName('')
            setValue('')
            setDescription('')
          })
          .finally(() => setSaving(false))
      }}
    >
      <Fields>
        <Field label="Name" hint="upper case, the way a shell expects it">
          <input
            required
            placeholder="API_KEY"
            value={name}
            onChange={(e) => setName(e.target.value.toUpperCase())}
          />
        </Field>
        <Field label="Kind" hint="a secret is shown once, at the moment you set it, and never again">
          <Select
            value={kind}
            aria-label="Kind"
            onChange={(v) => setKind(v as 'variable' | 'secret')}
            options={[
              { value: 'variable', label: 'variable — shown here afterwards' },
              { value: 'secret', label: 'secret — shown once, then never', disabled: !canStoreSecrets },
            ]}
          />
        </Field>
        <Field
          label="Value"
          wide
          hint={kind === 'secret' ? 'copy it somewhere safe before you save — you will not see it again' : undefined}
        >
          <input
            required
            type={kind === 'secret' ? 'password' : 'text'}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        </Field>
        <Field label="What it is for" wide hint="optional, and read by whoever inherits this install">
          <input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
      </Fields>
      <div className="row form-actions">
        <button type="submit" className="primary" disabled={saving}>
          {saving ? 'saving…' : 'set'}
        </button>
      </div>
    </form>
  )
}
