import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import CoalitionsPage from '../Coalitions'
import { GetCoalitions } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice'
import type { Coalition } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/state'

const mockCoalitions: Coalition[] = [
    { Name: 'Blue', Description: 'Blue side', Color: '#0000ff', Password: 'blue' },
    { Name: 'Red', Description: 'Red side', Color: '#ff0000', Password: 'red' },
]

beforeEach(() => {
    vi.mocked(GetCoalitions).mockResolvedValue(mockCoalitions)
})

describe('CoalitionsPage', () => {
    it('renders without crashing', async () => {
        const { container } = render(<CoalitionsPage />)
        await waitFor(() => expect(container.firstChild).toBeTruthy())
    })

    it('renders coalition entries after data loads', async () => {
        render(<CoalitionsPage />)
        await waitFor(() => {
            expect(screen.getByText('Blue')).toBeInTheDocument()
            expect(screen.getByText('Red')).toBeInTheDocument()
        })
    })

    it('renders empty list without crashing when no coalitions', async () => {
        vi.mocked(GetCoalitions).mockResolvedValue([])
        const { container } = render(<CoalitionsPage />)
        await waitFor(() => expect(container.firstChild).toBeTruthy())
        expect(screen.queryByText('Blue')).not.toBeInTheDocument()
    })

    it('renders the Add Coalition button', async () => {
        render(<CoalitionsPage />)
        await waitFor(() => expect(screen.getByRole('button', { name: /Add Coalition/i })).toBeInTheDocument())
    })

    it('opens the create dialog when Add Coalition is clicked', async () => {
        render(<CoalitionsPage />)
        await waitFor(() => screen.getByRole('button', { name: /Add Coalition/i }))
        await userEvent.click(screen.getByRole('button', { name: /Add Coalition/i }))
        await waitFor(() =>
            expect(screen.getByText(/Please enter the coalition details below/i)).toBeInTheDocument()
        )
    })
})
