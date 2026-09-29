import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ClientListPage from '../ClientList'
import { GetClients, IsClientMuted } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice'
import { GetCoalitionByName } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice'

const mockClients = {
    Clients: {
        'client-1': { UnitId: '10', Name: 'Pilot One', Coalition: 'Blue', Role: 1, LastUpdate: null },
        'client-2': { UnitId: '20', Name: 'Pilot Two', Coalition: 'Red', Role: 2, LastUpdate: null },
    },
}

beforeEach(() => {
    vi.mocked(GetClients).mockResolvedValue(mockClients)
    vi.mocked(IsClientMuted).mockResolvedValue(false)
    vi.mocked(GetCoalitionByName).mockResolvedValue({
        Name: 'Blue',
        Description: 'Blue side',
        Color: '#0000ff',
        Password: 'pass',
    })
})

describe('ClientListPage', () => {
    it('renders without crashing', async () => {
        const { container } = render(<ClientListPage />)
        await waitFor(() => expect(container.firstChild).toBeTruthy())
    })

    it('renders client entries after data loads', async () => {
        render(<ClientListPage />)
        await waitFor(() => {
            expect(screen.getByText(/Pilot One/)).toBeInTheDocument()
            expect(screen.getByText(/Pilot Two/)).toBeInTheDocument()
        })
    })

    it('renders empty list without crashing when no clients', async () => {
        vi.mocked(GetClients).mockResolvedValue({ Clients: {} })
        const { container } = render(<ClientListPage />)
        await waitFor(() => expect(container.firstChild).toBeTruthy())
        expect(screen.queryByText(/Pilot One/)).not.toBeInTheDocument()
    })

    it('opens the ban dialog when Ban is clicked', async () => {
        render(<ClientListPage />)
        await waitFor(() => screen.getAllByRole('button', { name: 'Ban' }))
        await userEvent.click(screen.getAllByRole('button', { name: 'Ban' })[0])
        await waitFor(() =>
            expect(screen.getByText(/You are attempting to ban a client/)).toBeInTheDocument()
        )
    })

    it('opens the kick dialog when Kick is clicked', async () => {
        render(<ClientListPage />)
        await waitFor(() => screen.getAllByRole('button', { name: 'Kick' }))
        await userEvent.click(screen.getAllByRole('button', { name: 'Kick' })[0])
        await waitFor(() =>
            expect(screen.getByText(/You are attempting to kick a client/)).toBeInTheDocument()
        )
    })
})
