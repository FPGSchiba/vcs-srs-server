import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ServerControls from '../ServerControl'
import { GetServerStatus, StartServer, StopServer } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/controlservice'
import { GetClients } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice'
import { GetSettings } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice'
import type { AdminState, SettingsState } from '../../bindings/github.com/FPGSchiba/vcs-srs-server/state'

const stoppedStatus: AdminState = {
  HTTPStatus: { IsRunning: false, IsNeeded: true, Error: '' },
  VoiceStatus: { IsRunning: false, IsNeeded: true, Error: '' },
  ControlStatus: { IsRunning: false, IsNeeded: true, Error: '' },
}

const runningStatus: AdminState = {
  HTTPStatus: { IsRunning: true, IsNeeded: true, Error: '' },
  VoiceStatus: { IsRunning: true, IsNeeded: true, Error: '' },
  ControlStatus: { IsRunning: true, IsNeeded: true, Error: '' },
}

const mockSettings: SettingsState = {
  General: { MaxRadiosPerUser: 5 },
  Servers: {
    HTTP: { Port: 14446, Host: '0.0.0.0' },
    Voice: { Port: 5002, Host: '0.0.0.0' },
    Control: { Port: 14448, Host: '0.0.0.0' },
  },
  Security: { EnableGuestAuth: true, EnablePluginAuth: false, Plugins: [], Token: { Expiration: 0, PrivateKeyFile: '', PublicKeyFile: '', Issuer: '', Subject: '' } },
  VoiceControl: { Port: 14448, ListenHost: '0.0.0.0', RemoteHost: 'localhost', CertificateFile: '', PrivateKeyFile: '' },
  Frequencies: { GlobalFrequencies: [], TestFrequencies: [] },
  Coalitions: [],
  Api: { Key: '' },
}

beforeEach(() => {
  vi.mocked(GetServerStatus).mockResolvedValue(stoppedStatus)
  vi.mocked(GetSettings).mockResolvedValue(mockSettings)
  vi.mocked(GetClients).mockResolvedValue({ Clients: {} })
  vi.mocked(StartServer).mockResolvedValue(undefined)
  vi.mocked(StopServer).mockResolvedValue(undefined)
})

describe('ServerControls', () => {
  it('renders without crashing', async () => {
    render(<ServerControls />)
    await waitFor(() => expect(screen.getByText('Server Status')).toBeInTheDocument())
  })

  it('shows Stopped chips when all servers are stopped', async () => {
    render(<ServerControls />)
    await waitFor(() => {
      const stoppedChips = screen.getAllByText('Stopped')
      expect(stoppedChips).toHaveLength(3)
    })
  })

  it('shows Running chips when all servers are running', async () => {
    vi.mocked(GetServerStatus).mockResolvedValue(runningStatus)
    render(<ServerControls />)
    await waitFor(() => {
      const runningChips = screen.getAllByText('Running')
      expect(runningChips).toHaveLength(3)
    })
  })

  it('shows "Start Servers" button when stopped', async () => {
    render(<ServerControls />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Start Servers' })).toBeInTheDocument())
  })

  it('shows "Stop Servers" button when running', async () => {
    vi.mocked(GetServerStatus).mockResolvedValue(runningStatus)
    render(<ServerControls />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Stop Servers' })).toBeInTheDocument())
  })

  it('calls StartServer when Start Servers is clicked', async () => {
    render(<ServerControls />)
    await waitFor(() => screen.getByRole('button', { name: 'Start Servers' }))
    await userEvent.click(screen.getByRole('button', { name: 'Start Servers' }))
    expect(StartServer).toHaveBeenCalledOnce()
  })

  it('displays server error messages when present', async () => {
    vi.mocked(GetServerStatus).mockResolvedValue({
      ...stoppedStatus,
      HTTPStatus: { IsRunning: false, IsNeeded: true, Error: 'port already in use' },
    })
    render(<ServerControls />)
    await waitFor(() =>
      expect(screen.getByText('HTTP Server Error: port already in use')).toBeInTheDocument()
    )
  })

  it('displays the port for each server from settings', async () => {
    render(<ServerControls />)
    await waitFor(() => {
      expect(screen.getByText('Port: 14446')).toBeInTheDocument()
      expect(screen.getByText('Port: 5002')).toBeInTheDocument()
      expect(screen.getByText('Port: 14448')).toBeInTheDocument()
    })
  })
})
