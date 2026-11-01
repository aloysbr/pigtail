import { useId } from 'react'

export interface CheckboxOption {
  value: string
  label: string
  /** Shown as the box's tooltip, never part of its name. */
  description?: string
}

/**
 * A group of native checkboxes under a legend (fields.css): controlled, in the order of `options`
 * whatever the order of the clicks. The label names each box; the keyboard is the browser's (Tab, Space).
 */
export function CheckboxGroup({
  legend,
  options,
  value,
  onChange,
  disabled,
  hideLegend,
}: {
  legend: string
  options: CheckboxOption[]
  value: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
  /** The legend still names the group for assistive tech but is not drawn (the caller already shows the question). */
  hideLegend?: boolean
}) {
  const uid = useId()
  const toggle = (v: string, on: boolean) => {
    const held = new Set(value)
    if (on) held.add(v)
    else held.delete(v)
    onChange(options.filter((o) => held.has(o.value)).map((o) => o.value))
  }
  return (
    <fieldset className="cbg" disabled={disabled}>
      <legend className={hideLegend ? 'cbg-l sr' : 'cbg-l'}>{legend}</legend>
      {options.map((o, i) => {
        const id = `${uid}-${i}`
        return (
          <div className="cbg-o" key={o.value}>
            <input id={id} type="checkbox" checked={value.includes(o.value)} onChange={(e) => toggle(o.value, e.target.checked)} />
            <label htmlFor={id} title={o.description}>
              {o.label}
            </label>
          </div>
        )
      })}
    </fieldset>
  )
}
