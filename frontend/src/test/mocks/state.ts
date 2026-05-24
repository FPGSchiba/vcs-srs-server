import { vi } from 'vitest'

export interface ServiceStatus {
  IsRunning: boolean
  IsNeeded: boolean
  Error: string
}

export interface AdminState {
  HTTPStatus: ServiceStatus
  VoiceStatus: ServiceStatus
  ControlStatus: ServiceStatus
}

export interface GeneralSettings {
  MaxRadiosPerUser: number
}

export interface ServerConfig {
  Port: number
  Host: string
}

export interface ServerSettings {
  HTTP: ServerConfig
  Voice: ServerConfig
  Control: ServerConfig
}

export interface SecuritySettings {
  EnableGuestAuth: boolean
  EnablePluginAuth: boolean
}

export interface VoiceControlSettings {
  Port: number
  ListenHost: string
  RemoteHost: string
  CertificateFile: string
  PrivateKeyFile: string
}

export class FrequencySettings {
  GlobalFrequencies: number[]
  TestFrequencies: number[]
  constructor(init: { GlobalFrequencies?: number[]; TestFrequencies?: number[] } = {}) {
    this.GlobalFrequencies = init.GlobalFrequencies ?? []
    this.TestFrequencies = init.TestFrequencies ?? []
  }
}

export interface FrequencySettingsInterface {
  GlobalFrequencies: number[]
  TestFrequencies: number[]
}

export interface SettingsState {
  General: GeneralSettings
  Servers: ServerSettings
  Security: SecuritySettings
  VoiceControl: VoiceControlSettings
  Frequencies: FrequencySettingsInterface
}

export interface ClientState {
  UnitId: string
  Name: string
  Coalition?: string
}

export interface RadioState {
  Muted: boolean
}

export interface BannedClient {
  id: string
  name: string
  reason: string
  ip_address: string
}

export interface Coalition {
  Name: string
  Description: string
  Color: string
  Password: string
}
