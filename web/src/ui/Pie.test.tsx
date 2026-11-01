import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Pie } from './Pie'

describe('Pie', () => {
  it('done=71 total=108 is an svg img named « 66 % fait » (the rounded fraction)', () => {
    render(<Pie done={71} total={108} />)
    const p = screen.getByRole('img', { name: '66 % fait' })
    expect(p.tagName.toLowerCase()).toBe('svg')
    expect(p.querySelector('path')).not.toBeNull()
  })

  it('a full pie is a disc, an empty one only the ring', () => {
    const { container, rerender } = render(<Pie done={4} total={4} />)
    expect(screen.getByRole('img', { name: '100 % fait' })).toBeInTheDocument()
    expect(container.querySelectorAll('circle')).toHaveLength(2)
    rerender(<Pie done={0} total={4} />)
    expect(screen.getByRole('img', { name: '0 % fait' })).toBeInTheDocument()
    expect(container.querySelectorAll('circle')).toHaveLength(1)
    expect(container.querySelector('path')).toBeNull()
  })

  it('total=0 renders nothing', () => {
    const { container } = render(<Pie done={0} total={0} />)
    expect(container).toBeEmptyDOMElement()
  })
})
