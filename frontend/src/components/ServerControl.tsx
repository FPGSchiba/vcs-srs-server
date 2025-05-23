import {GetServerStatus, StartServer, StopServer, GetSettings, GetClients} from '../../wailsjs/go/app/App'
import {useState, useEffect, JSX} from 'react'
import {Box, Button, Chip, Paper, Typography} from "@mui/material";
import {state} from "../../wailsjs/go/models";
import {EventsOn} from "../../wailsjs/runtime";


const ServerControls: () => JSX.Element = () => {
    const [status, setStatus] = useState<state.AdminState | null>(null);
    const [isLoading, setIsLoading] = useState(false);
    const [numClients , setNumClients] = useState(0);
    const [settings, setSettings] = useState<state.SettingsState | null>(null);

    const fetchStatus = async () => {
        try {
            const newStatus = await GetServerStatus();
            setStatus(newStatus);
        } catch (error) {
            console.error('Failed to fetch server status:', error);
        }
    };

    const fetchSettings = async () => {
        try {
            const newSettings = await GetSettings();
            setSettings(newSettings);
        } catch (error) {
            console.error('Failed to fetch settings:', error);
        }
    }

    const fetchNumClients = async () => {
        const clients = await GetClients();
        const numClients = Object.keys(clients.Clients).length;
        setNumClients(numClients);
    }

    const handleStatusChange = async (status: state.AdminState) => {
        setStatus(status);
    }

    const handleSettingsChange = async (settings: state.SettingsState) => {
        setSettings(settings);
    }

    EventsOn("admin/changed", handleStatusChange);
    EventsOn("settings/changed", handleSettingsChange);
    EventsOn("clients/changed", (clients: Record<string, state.ClientState>) => {
        const numClients = Object.keys(clients).length;
        setNumClients(numClients);
    })

    useEffect(() => {
        if (settings === null) {
            fetchSettings();

        }
        if (status === null) {
            fetchStatus();
        }
        if (numClients === 0) {
            fetchNumClients();
        }
    }, []);

    const handleServerToggle = async () => {
        setIsLoading(true);
        try {
            if (status?.HTTPStatus.IsRunning || status?.VoiceStatus.IsRunning || status?.ControlStatus.IsRunning) {
                await StopServer();
            } else {
                await StartServer();
            }
            await fetchStatus();
        } catch (error) {
            console.error('Failed to toggle server:', error);
        } finally {
            setIsLoading(false);
        }
    };

    const isRunning = status?.HTTPStatus.IsRunning || status?.VoiceStatus.IsRunning || status?.ControlStatus.IsRunning;

    return (
        <Paper className="control control-container">

            <div className="control control-general control-general-container">
                <Typography className="control control-general control-general-title" variant={"h4"}>Server Status</Typography>
                <div className="control control-general control-general-items">
                    <Chip className={`control control-general control-general-item`} label={`Clients: ${numClients}`} />
                </div>

            </div>
            <div className="control control-content">
                <Box className="control control-box control-box-mid">
                    <div className="control control-items">
                        <div className="control control-item control-item-container">
                            <span className="control control-item control-item-title">HTTP Server:</span>
                            <Chip className={`control control-item control-item-${status?.HTTPStatus.IsRunning ? 'running' : 'stopped'}`} label={status?.HTTPStatus.IsRunning ? 'Running' : 'Stopped'} />
                            <Chip className={`control control-item control-item-port`} label={`Port: ${settings?.Servers.HTTP.Port}`} />
                        </div>
                        <div className="control control-item control-item-container">
                            <span className="control control-item control-item-title">Voice Server:</span>
                            <Chip className={`control control-item control-item-${status?.VoiceStatus.IsRunning ? 'running' : 'stopped'}`} label={status?.VoiceStatus.IsRunning ? 'Running' : 'Stopped'} />
                            <Chip className={`control control-item control-item-port`} label={`Port: ${settings?.Servers.Voice.Port}`} />
                        </div>
                        <div className="control control-item control-item-container">
                            <span className="control control-item control-item-title">Control Server:</span>
                            <Chip className={`control control-item control-item-${status?.ControlStatus.IsRunning ? 'running' : 'stopped'}`} label={status?.ControlStatus.IsRunning ? 'Running' : 'Stopped'} />
                            <Chip className={`control control-item control-item-port`} label={`Port: ${settings?.Servers.Control.Port}`} />
                        </div>
                    </div>
                    <Button
                        variant="contained"
                        onClick={handleServerToggle}
                        disabled={isLoading}
                        color={isRunning ? 'error' : 'primary'}
                        className={`control control-button ${isRunning ? 'running' : 'stopped'}`}
                    >
                        {isLoading ? 'Processing...' : isRunning ? 'Stop Servers' : 'Start Servers'}
                    </Button>
                </Box>
                <Box className="control control-box control-box-right">

                    <div className="control control-errors">
                        {status?.HTTPStatus.Error && <div className="control control-error">HTTP Server Error: {status.HTTPStatus.Error}</div>}
                        {status?.VoiceStatus.Error && <div className="control control-error">Voice Server Error: {status.VoiceStatus.Error}</div>}
                        {status?.ControlStatus.Error && <div className="control control-error">Control Server Error: {status.ControlStatus.Error}</div>}
                    </div>
                </Box>
            </div>
        </Paper>
    );
};

export default ServerControls;