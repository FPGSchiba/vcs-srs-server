import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import Header from '../Header'

describe('Header', () => {
  it('renders without crashing', () => {
    render(<Header onOpenLogs={vi.fn()} />)
    expect(screen.getByRole('banner')).toBeInTheDocument()
  })

  it('calls onOpenLogs when the logs button is clicked', async () => {
    const onOpenLogs = vi.fn()
    render(<Header onOpenLogs={onOpenLogs} />)
    await userEvent.click(screen.getByTitle('Logs'))
    expect(onOpenLogs).toHaveBeenCalledOnce()
  })
})
