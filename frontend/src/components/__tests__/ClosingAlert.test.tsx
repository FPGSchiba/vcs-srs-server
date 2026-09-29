import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ClosingAlert } from '../ClosingAlert'
import type { Notification } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/events'

const note: Notification = {
    id: 'n-1',
    title: 'Test Title',
    message: 'Test message',
    level: 'info',
}

describe('ClosingAlert', () => {
    it('renders without crashing', () => {
        render(<ClosingAlert notification={note} closeNotification={vi.fn()} />)
        expect(screen.getByText('Test Title')).toBeInTheDocument()
    })

    it('renders the notification message', () => {
        render(<ClosingAlert notification={note} closeNotification={vi.fn()} />)
        expect(screen.getByText('Test message')).toBeInTheDocument()
    })

    it('calls closeNotification with the notification id when close button is clicked', async () => {
        const close = vi.fn()
        render(<ClosingAlert notification={note} closeNotification={close} />)
        await userEvent.click(screen.getByLabelText('close'))
        expect(close).toHaveBeenCalledWith('n-1')
    })
})
