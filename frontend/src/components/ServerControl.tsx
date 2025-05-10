import { GetServerStatus, StartServer, StopServer } from '../../wailsjs/go/main/App'
import { useState, useEffect } from 'react'

interface ServiceStatus {
    IsRunning: boolean;
    Error: string;
}

interface ServerStatus {
    http: ServiceStatus;
    voice: ServiceStatus;
}

const ServerControls: React.FC = () => {
    const [status, setStatus] = useState<ServerStatus | null>(null);
    const [isLoading, setIsLoading] = useState(false);

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
        const interval = setInterval(fetchStatus, 1000);
        return () => clearInterval(interval);
    }, []);

    const handleServerToggle = async () => {
        setIsLoading(true);
        try {
            if (status?.http.IsRunning || status?.voice.IsRunning) {
                await StopServer();
            } else {
                await StartServer();
            }
            // Fetch status immediately after toggle
            await fetchStatus();
        } catch (error) {
            console.error('Failed to toggle server:', error);
        } finally {
            setIsLoading(false);
        }
    };

    const isRunning = status?.http.IsRunning || status?.voice.IsRunning;

    return (
        <div className="server-controls">
            <div className="control-group">
                <h3>Server Status</h3>
                <div className="status-indicators">
                    <div className="status-item">
                        <span>HTTP Server:</span>
                        <span className={status?.http.IsRunning ? 'running' : 'stopped'}>
                            {status?.http.IsRunning ? 'Running' : 'Stopped'}
                        </span>
                    </div>
                    <div className="status-item">
                        <span>Voice Server:</span>
                        <span className={status?.voice.IsRunning ? 'running' : 'stopped'}>
                            {status?.voice.IsRunning ? 'Running' : 'Stopped'}
                        </span>
                    </div>
                </div>
                <button
                    onClick={handleServerToggle}
                    disabled={isLoading}
                    className={`toggle-button ${isRunning ? 'running' : 'stopped'}`}
                >
                    {isLoading ? 'Processing...' : (isRunning ? 'Stop Servers' : 'Start Servers')}
                </button>
                {(status?.http.Error || status?.voice.Error) && (
                    <div className="error-container">
                        {status.http.Error && <div className="error">HTTP Error: {status.http.Error}</div>}
                        {status.voice.Error && <div className="error">Voice Error: {status.voice.Error}</div>}
                    </div>
                )}
            </div>
        </div>
    );
};

export default ServerControls;