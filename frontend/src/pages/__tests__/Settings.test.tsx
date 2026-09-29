import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import SettingsPage from '../Settings'
import {
  GetSettings,
  SaveGeneralSettings,
  SaveServerSettings,
  SaveSecuritySettings,
  SaveVoiceControlSettings,
} from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice'
import type { SettingsState } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/state'

const mockSettings: SettingsState = {
  General: { MaxRadiosPerUser: 5 },
  Servers: {
    HTTP: { Port: 14446, Host: '0.0.0.0' },
    Voice: { Port: 5002, Host: '0.0.0.0' },
    Control: { Port: 14448, Host: '0.0.0.0' },
  },
  Security: {
    EnableGuestAuth: true,
    EnablePluginAuth: false,
    Plugins: [],
    Token: {
      Expiration: 0,
      PrivateKeyFile: '',
      PublicKeyFile: '',
      Issuer: '',
      Subject: '',
    },
  },
  VoiceControl: {
    Port: 14448,
    ListenHost: '0.0.0.0',
    RemoteHost: 'localhost',
    CertificateFile: '',
    PrivateKeyFile: '',
  },
  Frequencies: { GlobalFrequencies: [], TestFrequencies: [] },
  Coalitions: [],
  Api: { Key: '' },
}

beforeEach(() => {
  vi.mocked(GetSettings).mockResolvedValue(mockSettings)
  vi.mocked(SaveGeneralSettings).mockResolvedValue(undefined)
  vi.mocked(SaveServerSettings).mockResolvedValue(undefined)
  vi.mocked(SaveSecuritySettings).mockResolvedValue(undefined)
  vi.mocked(SaveVoiceControlSettings).mockResolvedValue(undefined)
})

describe('SettingsPage', () => {
  it('renders without crashing', async () => {
    render(<SettingsPage />)
    await waitFor(() => expect(screen.getByText('General')).toBeInTheDocument())
  })

  it('renders all section headings', async () => {
    render(<SettingsPage />)
    await waitFor(() => {
      expect(screen.getByText('General')).toBeInTheDocument()
      expect(screen.getByText('Servers')).toBeInTheDocument()
      expect(screen.getByText('Security')).toBeInTheDocument()
      expect(screen.getByText('Voice Control')).toBeInTheDocument()
    })
  })

  it('renders the Save button', async () => {
    render(<SettingsPage />)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
    )
  })

  it('shows a Zod validation error when MaxRadiosPerUser is set to 0', async () => {
    render(<SettingsPage />)

    // Wait for settings to load (GetSettings resolves and reset() is called)
    await waitFor(() =>
      expect(screen.getByText('Max Number of Radios per User')).toBeInTheDocument()
    )

    // The MaxRadiosPerUser field is the first spinbutton on the page
    const spinbuttons = screen.getAllByRole('spinbutton')
    const maxRadiosInput = spinbuttons[0]

    await userEvent.clear(maxRadiosInput)
    await userEvent.type(maxRadiosInput, '0')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(screen.getByText('Must be at least 1')).toBeInTheDocument()
    )
  })

  it('calls all save functions on valid form submission', async () => {
    render(<SettingsPage />)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
    )

    await userEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(SaveGeneralSettings).toHaveBeenCalledOnce()
      expect(SaveServerSettings).toHaveBeenCalledOnce()
      expect(SaveSecuritySettings).toHaveBeenCalledOnce()
      expect(SaveVoiceControlSettings).toHaveBeenCalledOnce()
    })
  })
})
