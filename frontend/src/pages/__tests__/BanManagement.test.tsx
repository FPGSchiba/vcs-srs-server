import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import BanManagement from '../BanManagement'
import { GetBannedClients, UnbanClient } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice'
import type { BannedClient } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/state'

const mockBanned: BannedClient[] = [
  { id: 'abc-1', name: 'Pilot One', reason: 'griefing', ip_address: '10.0.0.1' },
  { id: 'abc-2', name: 'Pilot Two', reason: 'cheating', ip_address: '10.0.0.2' },
]

beforeEach(() => {
  vi.mocked(GetBannedClients).mockResolvedValue(mockBanned)
  vi.mocked(UnbanClient).mockResolvedValue(undefined)
})

describe('BanManagement', () => {
  it('renders without crashing', async () => {
    render(<BanManagement />)
    await waitFor(() => expect(screen.getByText('Pilot One')).toBeInTheDocument())
  })

  it('renders all banned clients', async () => {
    render(<BanManagement />)
    await waitFor(() => {
      expect(screen.getByText('Pilot One')).toBeInTheDocument()
      expect(screen.getByText('Pilot Two')).toBeInTheDocument()
    })
  })

  it('shows ban reason and IP for each client', async () => {
    render(<BanManagement />)
    await waitFor(() => {
      expect(screen.getByText(/griefing/)).toBeInTheDocument()
      expect(screen.getByText(/10\.0\.0\.1/)).toBeInTheDocument()
    })
  })

  it('renders empty state without crashing when no banned clients', async () => {
    vi.mocked(GetBannedClients).mockResolvedValue([])
    const { container } = render(<BanManagement />)
    await waitFor(() => expect(container.firstChild).toBeTruthy())
    expect(screen.queryByText('Pilot One')).not.toBeInTheDocument()
  })

  it('calls UnbanClient when Unban is clicked', async () => {
    render(<BanManagement />)
    await waitFor(() => screen.getAllByRole('button', { name: /[Uu]nban/ }))
    await userEvent.click(screen.getAllByRole('button', { name: /[Uu]nban/ })[0])
    expect(UnbanClient).toHaveBeenCalledWith('abc-1')
  })
})
