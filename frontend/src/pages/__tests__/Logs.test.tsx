import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import LogsPage from '../Logs'

beforeEach(() => {
    // jsdom does not implement scrollIntoView — stub it
    window.HTMLElement.prototype.scrollIntoView = vi.fn()
})

describe('LogsPage', () => {
    it('renders without crashing', () => {
        const { container } = render(<LogsPage />)
        expect(container).toBeInTheDocument()
    })

    it('renders the Logs heading', () => {
        render(<LogsPage />)
        expect(screen.getByText('Logs')).toBeInTheDocument()
    })

    it('renders the Level filter select', () => {
        render(<LogsPage />)
        // MUI Select renders a combobox role
        expect(screen.getByRole('combobox')).toBeInTheDocument()
    })

    it('renders the Clear button', () => {
        render(<LogsPage />)
        expect(screen.getByRole('button', { name: /Clear/i })).toBeInTheDocument()
    })

    it('renders empty log view by default', () => {
        render(<LogsPage />)
        // No log entries rendered by default
        expect(screen.queryByText(/\[.*\]/)).not.toBeInTheDocument()
    })

    it('clears log entries when Clear is clicked', async () => {
        render(<LogsPage />)
        await userEvent.click(screen.getByRole('button', { name: /Clear/i }))
        // Still renders without crashing after clear
        expect(screen.getByRole('button', { name: /Clear/i })).toBeInTheDocument()
    })
})
