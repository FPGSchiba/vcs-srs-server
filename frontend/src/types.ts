export interface ServiceStatus {
    IsRunning: boolean;
    Error: string;
}

export interface ServerStatus {
    http: ServiceStatus;
    voice: ServiceStatus;
    control: ServiceStatus;
}

export interface SettingsState {
    servers: ServerSettings;
    coalitions: Coalition[];
}

export interface ServerSettings {
    http: ServerSetting;
    voice: ServerSetting;
    control: ServerSetting;
}

export interface ServerSetting {
    host: string;
    port: number;
}

export interface Coalition {
    name: string;
    description: string;
    color: string;
    password: string;
}