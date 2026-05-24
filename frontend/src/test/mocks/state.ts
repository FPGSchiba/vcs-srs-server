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

export interface PluginFlowSettings {
  FlowID: string
  Configuration: Record<string, string>
}

export interface FlowConfiguration {
  Flows: PluginFlowSettings[]
  GlobalSettings: Record<string, string> | null
}

export interface PluginSettings {
  Name: string
  Enabled: boolean
  Address: string
  CertificateFile: string
  Configurations: FlowConfiguration
}

export interface TokenSettings {
  Expiration: number
  PrivateKeyFile: string
  PublicKeyFile: string
  Issuer: string
  Subject: string
}

export interface SecuritySettings {
  EnableGuestAuth: boolean
  EnablePluginAuth: boolean
  Plugins: PluginSettings[]
  Token: TokenSettings
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

export interface ApiSettings {
  Key: string
}

export interface SettingsState {
  General: GeneralSettings
  Servers: ServerSettings
  Security: SecuritySettings
  VoiceControl: VoiceControlSettings
  Frequencies: FrequencySettingsInterface
  Coalitions: Coalition[]
  Api: ApiSettings
}

export interface ClientState {
  UnitId: string
  Name: string
  Coalition: string
  Role: number
  LastUpdate: string | null
}

export interface Radio {
  ID: number
  Name: string
  Frequency: number
  Enabled: boolean
  IsIntercom: boolean
}

export interface RadioState {
  Radios: Radio[]
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
