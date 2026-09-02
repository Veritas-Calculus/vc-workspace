import { useId } from 'react'

type FormFieldProps = {
  label: string
  value: string
  onChange: (value: string) => void
  type?: string
  autoComplete: string
  required?: boolean
  min?: number
  max?: number
  pattern?: string
  minLength?: number
  maxLength?: number
  hint?: string
  error?: string
}

/** Replaces repeated label/input wiring so hints and errors cannot drift away from their control. */
export function FormField({ label, value, onChange, type = 'text', autoComplete, required = true, min, max, pattern, minLength, maxLength, hint, error }: FormFieldProps) {
  const id = useId()
  const hintID = `${id}-hint`
  const errorID = `${id}-error`
  const describedBy = [hint ? hintID : '', error ? errorID : ''].filter(Boolean).join(' ') || undefined
  return (
    <div className="form-field">
      <label htmlFor={id}>{label}</label>
      <input id={id} type={type} value={value} onChange={(event) => onChange(event.target.value)} autoComplete={autoComplete} required={required} min={min} max={max} pattern={pattern} minLength={minLength} maxLength={maxLength} aria-invalid={error ? true : undefined} aria-describedby={describedBy} />
      {hint && <p className="field-hint" id={hintID}>{hint}</p>}
      {error && <p className="field-error" id={errorID} role="alert">{error}</p>}
    </div>
  )
}
