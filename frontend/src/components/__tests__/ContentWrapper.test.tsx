import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import ContentWrapper from '../ContentWrapper'
import { GetSettings } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice'
import { GetCoalitions } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice'
import { GetClients, GetBannedClients } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice'

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
    Frequencies: { GlobalFrequencies: [], TestFrequencies: [] },
    Coalitions: [],
    Api: { Key: '' },
}

beforeEach(() => {
    vi.mocked(GetSettings).mockResolvedValue(mockSettings)
    vi.mocked(GetCoalitions).mockResolvedValue([])
    vi.mocked(GetClients).mockResolvedValue({ Clients: {} })
    vi.mocked(GetBannedClients).mockResolvedValue([])
})

describe('ContentWrapper', () => {
    it('renders without crashing', () => {
        const { container } = render(<ContentWrapper />)
        expect(container).toBeInTheDocument()
    })

    it('renders the Settings tab by default', () => {
        render(<ContentWrapper />)
        expect(screen.getByRole('tab', { name: 'Settings' })).toBeInTheDocument()
    })

    it('renders all navigation tabs', () => {
        render(<ContentWrapper />)
        expect(screen.getByRole('tab', { name: 'Settings' })).toBeInTheDocument()
        expect(screen.getByRole('tab', { name: 'Coalitions' })).toBeInTheDocument()
        expect(screen.getByRole('tab', { name: 'Clients' })).toBeInTheDocument()
        expect(screen.getByRole('tab', { name: 'Banned Clients' })).toBeInTheDocument()
        expect(screen.getByRole('tab', { name: 'Frequencies' })).toBeInTheDocument()
    })
})
