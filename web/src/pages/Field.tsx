import type { ReactNode } from 'react'

/**
 * A labelled field.
 *
 * The pattern this replaces put the field's NAME and an EXAMPLE of its value
 * into one placeholder — "name (e.g. anthropic, ollama)", "base URL (default:
 * api.anthropic.com)". That fails twice over, and the second failure is the
 * one that matters.
 *
 * It fails visually, because a placeholder is clipped by the input rather than
 * wrapped: in a row of three fields the operator reads "name (e.g. anthropic,
 * o" and "API key (optional for lo", which names neither the field nor the
 * example.
 *
 * And it fails structurally, because a placeholder disappears the moment you
 * type into it. The label is gone at exactly the point you would want to check
 * you are filling in the right box — and for anyone tabbing through the form
 * with a screen reader there was no label at any point, only a hint that
 * assistive technology is free to ignore.
 *
 * So: the name is a real <label>, above, always there. The example is a hint
 * below, in smaller type, where it can wrap to as many lines as it needs. The
 * placeholder is either empty or a bare specimen value — never a sentence.
 */
export function Field({
  label,
  hint,
  wide,
  children,
}: {
  label: string
  /** an example, a default, or what happens if it is left empty */
  hint?: ReactNode
  /** take the whole row rather than sharing it */
  wide?: boolean
  children: ReactNode
}) {
  return (
    <label className={`field ${wide ? 'field-wide' : ''}`}>
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  )
}

/** A row of fields that wraps instead of squeezing them. */
export function Fields({ children }: { children: ReactNode }) {
  return <div className="fields">{children}</div>
}
