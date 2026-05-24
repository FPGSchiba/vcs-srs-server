import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import LogsDialog from '../LogsDialog'

beforeEach(() => {
    // jsdom does not implement scrollIntoView — stub it (used by LogsPage inside LogsDialog)
    window.HTMLElement.prototype.scrollIntoView = vi.fn()
})

describe('LogsDialog', () => {
    it('renders without crashing when open', () => {
        render(<LogsDialog open={true} onClose={vi.fn()} />)
        // Both the DialogTitle and the inner LogsPage heading contain "Logs"
        expect(screen.getAllByText('Logs').length).toBeGreaterThan(0)
    })

    it('renders without crashing when closed', () => {
        const { container } = render(<LogsDialog open={false} onClose={vi.fn()} />)
        expect(container).toBeInTheDocument()
    })

    it('renders a close icon button when open', () => {
        render(<LogsDialog open={true} onClose={vi.fn()} />)
        const closeButtons = screen.getAllByRole('button')
        expect(closeButtons.length).toBeGreaterThan(0)
    })

    it('calls onClose when the close button is clicked', async () => {
        const onClose = vi.fn()
        render(<LogsDialog open={true} onClose={onClose} />)
        // The title-bar close button (first button in the title area)
        const buttons = screen.getAllByRole('button')
        await userEvent.click(buttons[0])
        expect(onClose).toHaveBeenCalled()
    })
})
