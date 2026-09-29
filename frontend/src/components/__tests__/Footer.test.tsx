import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import Footer from '../Footer'
import { GetServerVersion } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/controlservice'

beforeEach(() => {
    vi.mocked(GetServerVersion).mockResolvedValue('v1.0.0')
})

describe('Footer', () => {
    it('renders without crashing', async () => {
        render(<Footer />)
        await waitFor(() => expect(screen.getByText('VCS Server')).toBeInTheDocument())
    })

    it('displays the version returned by GetServerVersion', async () => {
        vi.mocked(GetServerVersion).mockResolvedValue('v2.3.4')
        render(<Footer />)
        await waitFor(() => expect(screen.getByText('v2.3.4')).toBeInTheDocument())
    })
})
