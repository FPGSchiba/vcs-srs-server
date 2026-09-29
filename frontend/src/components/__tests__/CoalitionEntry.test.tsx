import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import CoalitionEntry from '../CoalitionEntry'
import type { Coalition } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/state'
import { UpdateCoalition } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice'

const mockCoalition: Coalition = {
    Name: 'Blue Coalition',
    Description: 'The blue team',
    Color: '#0000ff',
    Password: 'blue123',
}

beforeEach(() => {
    vi.mocked(UpdateCoalition).mockResolvedValue(undefined)
})

describe('CoalitionEntry', () => {
    it('renders without crashing', () => {
        render(<CoalitionEntry coalition={mockCoalition} openDeleteDialog={vi.fn()} />)
        expect(screen.getByText('Blue Coalition')).toBeInTheDocument()
    })

    it('shows the coalition name as a heading', () => {
        render(<CoalitionEntry coalition={mockCoalition} openDeleteDialog={vi.fn()} />)
        expect(screen.getByText('Blue Coalition')).toBeInTheDocument()
    })

    it('pre-fills the description field', () => {
        render(<CoalitionEntry coalition={mockCoalition} openDeleteDialog={vi.fn()} />)
        const input = screen.getByDisplayValue('The blue team')
        expect(input).toBeInTheDocument()
    })

    it('pre-fills the password field', () => {
        render(<CoalitionEntry coalition={mockCoalition} openDeleteDialog={vi.fn()} />)
        const input = screen.getByDisplayValue('blue123')
        expect(input).toBeInTheDocument()
    })

    it('renders Save and Delete buttons', () => {
        render(<CoalitionEntry coalition={mockCoalition} openDeleteDialog={vi.fn()} />)
        expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
    })

    it('calls openDeleteDialog when Delete is clicked', async () => {
        const openDeleteDialog = vi.fn()
        render(<CoalitionEntry coalition={mockCoalition} openDeleteDialog={openDeleteDialog} />)
        await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
        expect(openDeleteDialog).toHaveBeenCalledOnce()
    })
})
