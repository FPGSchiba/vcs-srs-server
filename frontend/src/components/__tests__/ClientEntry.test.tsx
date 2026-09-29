import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ClientEntry from '../ClientEntry'
import type { ClientState } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/state'
import { GetCoalitionByName } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice'
import { IsClientMuted, MuteClient, UnmuteClient } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice'

const mockClient: ClientState = {
    UnitId: '42',
    Name: 'Pilot Alpha',
    Coalition: 'Blue',
    Role: 1,
    LastUpdate: null,
}

beforeEach(() => {
    vi.mocked(GetCoalitionByName).mockResolvedValue({
        Name: 'Blue',
        Description: 'Blue coalition',
        Color: '#0000ff',
        Password: 'secret',
    })
    vi.mocked(IsClientMuted).mockResolvedValue(false)
    vi.mocked(MuteClient).mockResolvedValue(undefined)
    vi.mocked(UnmuteClient).mockResolvedValue(undefined)
})

describe('ClientEntry', () => {
    it('renders without crashing', () => {
        render(<ClientEntry client={mockClient} clientId="id-1" handleBan={vi.fn()} handleKick={vi.fn()} />)
        expect(screen.getByText(/Pilot Alpha/)).toBeInTheDocument()
    })

    it('displays the client unit id and name', () => {
        render(<ClientEntry client={mockClient} clientId="id-1" handleBan={vi.fn()} handleKick={vi.fn()} />)
        expect(screen.getByText(/\[42\].*Pilot Alpha/)).toBeInTheDocument()
    })

    it('renders Kick and Ban action buttons', () => {
        render(<ClientEntry client={mockClient} clientId="id-1" handleBan={vi.fn()} handleKick={vi.fn()} />)
        expect(screen.getByRole('button', { name: 'Kick' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Ban' })).toBeInTheDocument()
    })

    it('calls handleKick when Kick is clicked', async () => {
        const handleKick = vi.fn()
        render(<ClientEntry client={mockClient} clientId="id-1" handleBan={vi.fn()} handleKick={handleKick} />)
        await userEvent.click(screen.getByRole('button', { name: 'Kick' }))
        expect(handleKick).toHaveBeenCalledWith('id-1')
    })

    it('calls handleBan when Ban is clicked', async () => {
        const handleBan = vi.fn()
        render(<ClientEntry client={mockClient} clientId="id-1" handleBan={handleBan} handleKick={vi.fn()} />)
        await userEvent.click(screen.getByRole('button', { name: 'Ban' }))
        expect(handleBan).toHaveBeenCalledWith('id-1')
    })

    it('renders Mute button when client is not muted', () => {
        vi.mocked(IsClientMuted).mockResolvedValue(false)
        render(<ClientEntry client={mockClient} clientId="id-1" handleBan={vi.fn()} handleKick={vi.fn()} />)
        expect(screen.getByRole('button', { name: 'Mute' })).toBeInTheDocument()
    })
})
