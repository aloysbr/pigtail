import { useId } from 'react'

/**
 * A full-width text field with its label bound to it (fields.css): controlled; Enter submits a non empty value
 * when `onSubmit` is given. The label shows unless `hideLabel` (then it is read, not seen).
 */
export function TextField({
  label,
  value,
  onChange,
  onSubmit,
  placeholder,
  hideLabel,
  disabled,
  autoFocus,
}: {
  label: string
  value: string
  onChange: (next: string) => void
  onSubmit?: (value: string) => void
  placeholder?: string
  hideLabel?: boolean
  disabled?: boolean
  autoFocus?: boolean
}) {
  const id = useId()
  return (
    <div className="tf">
      <label className={hideLabel ? 'tf-l sr' : 'tf-l'} htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        className="tf-i"
        type="text"
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        autoFocus={autoFocus}
        autoComplete="off"
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && onSubmit && value.trim() !== '' && !e.nativeEvent.isComposing) {
            e.preventDefault()
            onSubmit(value)
          }
        }}
      />
    </div>
  )
}
