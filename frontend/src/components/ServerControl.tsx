import { GetServerStatus, StartServer, StopServer } from '../../wailsjs/go/app/App'
import { useState, useEffect } from 'react'
import {Box, Button, Chip, Paper, Typography} from "@mui/material";

interface ServiceStatus {
    IsRunning: boolean;
    Error: string;
}

interface ServerStatus {
    http: ServiceStatus;
    voice: ServiceStatus;
    control: ServiceStatus; // Add control
}

const ServerControls: React.FC = () => {
    const [status, setStatus] = useState<ServerStatus | null>(null);
    const [isLoading, setIsLoading] = useState(false);
    const [numClients , setNumClients] = useState(0);
    const [port, setPort] = useState(5002);

    const fetchStatus = async () => {
        try {
            const newStatus = await GetServerStatus();
            setStatus(newStatus as ServerStatus);
        } catch (error) {
            console.error('Failed to fetch server status:', error);
        }
    };

    useEffect(() => {
        fetchStatus();
        const interval = setInterval(fetchStatus, 3000);
        return () => clearInterval(interval);
    }, []);

    const handleServerToggle = async () => {
        setIsLoading(true);
        try {
            if (status?.http.IsRunning || status?.voice.IsRunning || status?.control.IsRunning) {
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

    const isRunning = status?.http.IsRunning || status?.voice.IsRunning || status?.control.IsRunning;

    return (
        <Paper className="control control-container">

            <div className="control control-general control-general-container">
                <Typography className="control control-general control-general-title" variant={"h4"}>Server Status</Typography>
                <div className="control control-general control-general-items">
                    <Chip className={`control control-general control-general-item`} label={`Port: ${port}`} />
                    <Chip className={`control control-general control-general-item`} label={`Clients: ${numClients}`} />
                </div>

            </div>
            <div className="control control-content">
                <Box className="control control-box control-box-mid">
                    <div className="control control-items">
                        <div className="control control-item control-item-container">
                            <span className="control control-item control-item-title">HTTP Server:</span>
                            <Chip className={`control control-item control-item-${status?.http.IsRunning ? 'running' : 'stopped'}`} label={status?.http.IsRunning ? 'Running' : 'Stopped'} />
                        </div>
                        <div className="control control-item control-item-container">
                            <span className="control control-item control-item-title">Voice Server:</span>
                            <Chip className={`control control-item control-item-${status?.voice.IsRunning ? 'running' : 'stopped'}`} label={status?.voice.IsRunning ? 'Running' : 'Stopped'} />
                        </div>
                        <div className="control control-item control-item-container">
                            <span className="control control-item control-item-title">Control Server:</span>
                            <Chip className={`control control-item control-item-${status?.control.IsRunning ? 'running' : 'stopped'}`} label={status?.control.IsRunning ? 'Running' : 'Stopped'} />
                        </div>
                    </div>
                    <Button
                        variant="contained"
                        onClick={handleServerToggle}
                        disabled={isLoading}
                        className={`control control-button ${isRunning ? 'running' : 'stopped'}`}
                    >
                        {isLoading ? 'Processing...' : isRunning ? 'Stop Servers' : 'Start Servers'}
                    </Button>
                </Box>
                <Box className="control control-box control-box-right">

                    <div className="control control-errors">
                        {status?.http.Error && <div className="control control-error">HTTP Server Error: {status.http.Error}</div>}
                        {status?.voice.Error && <div className="control control-error">Voice Server Error: {status.voice.Error}</div>}
                        {status?.control.Error && <div className="control control-error">Control Server Error: {status.control.Error}</div>}
                    </div>
                </Box>
            </div>
        </Paper>
    );
};

export default ServerControls;