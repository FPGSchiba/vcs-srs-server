import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import FrequencyPage from '../FrequencyPage'
import { GetSettings, SaveFrequencySettings } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice'

const mockSettings = {
    General: { MaxRadiosPerUser: 4 },
    Servers: {
        HTTP: { Port: 8080, Host: '0.0.0.0' },
        Voice: { Port: 5002, Host: '0.0.0.0' },
        Control: { Port: 5003, Host: '0.0.0.0' },
    },
    Security: {
        EnableGuestAuth: false,
        EnablePluginAuth: false,
        Plugins: [],
        Token: {
            Expiration: 3600,
            PrivateKeyFile: '',
            PublicKeyFile: '',
            Issuer: '',
            Subject: '',
        },
    },
    VoiceControl: {
        Port: 5010,
        ListenHost: '0.0.0.0',
        RemoteHost: '0.0.0.0',
        CertificateFile: '',
        PrivateKeyFile: '',
    },
    Frequencies: {
        GlobalFrequencies: [124.5, 130.0],
        TestFrequencies: [121.5],
    },
    Coalitions: [],
    Api: { Key: '' },
}

beforeEach(() => {
    vi.mocked(GetSettings).mockResolvedValue(mockSettings)
    vi.mocked(SaveFrequencySettings).mockResolvedValue(undefined)
})

describe('FrequencyPage', () => {
    it('renders without crashing', async () => {
        const { container } = render(<FrequencyPage />)
        await waitFor(() => expect(container.firstChild).toBeTruthy())
    })

    it('renders Global Frequencies and Test Frequencies subheaders', async () => {
        render(<FrequencyPage />)
        await waitFor(() => {
            expect(screen.getByText('Global Frequencies')).toBeInTheDocument()
            expect(screen.getByText('Test Frequencies')).toBeInTheDocument()
        })
    })

    it('displays loaded global frequencies', async () => {
        render(<FrequencyPage />)
        await waitFor(() => {
            // 124.5 → formatted as 124.500
            expect(screen.getByText('124.500')).toBeInTheDocument()
        })
    })

    it('displays loaded test frequencies', async () => {
        render(<FrequencyPage />)
        await waitFor(() => {
            // 121.5 → formatted as 121.500
            expect(screen.getByText('121.500')).toBeInTheDocument()
        })
    })

    it('renders the Add Frequency and Save buttons', async () => {
        render(<FrequencyPage />)
        await waitFor(() => {
            expect(screen.getByRole('button', { name: /Add Frequency/i })).toBeInTheDocument()
            expect(screen.getByRole('button', { name: /Save/i })).toBeInTheDocument()
        })
    })

    it('opens the add frequency dialog when Add Frequency is clicked', async () => {
        render(<FrequencyPage />)
        await waitFor(() => screen.getByRole('button', { name: /Add Frequency/i }))
        await userEvent.click(screen.getByRole('button', { name: /Add Frequency/i }))
        await waitFor(() =>
            expect(screen.getByText(/Add a new frequency to the list/i)).toBeInTheDocument()
        )
    })

    it('calls SaveFrequencySettings when Save is clicked', async () => {
        render(<FrequencyPage />)
        await waitFor(() => screen.getByRole('button', { name: /Save/i }))
        await userEvent.click(screen.getByRole('button', { name: /Save/i }))
        await waitFor(() => expect(SaveFrequencySettings).toHaveBeenCalled())
    })
})
