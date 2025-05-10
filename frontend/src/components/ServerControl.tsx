import { GetServerStatus, StartServer, StopServer } from '../../wailsjs/go/app/App'
import { useState, useEffect } from 'react'

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
                    <div className="status-item">
                        <span>Control Server:</span>
                        <span className={status?.control.IsRunning ? 'running' : 'stopped'}>
                            {status?.control.IsRunning ? 'Running' : 'Stopped'}
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
                {(status?.http.Error || status?.voice.Error || status?.control.Error) && (
                    <div className="error-container">
                        {status?.http.Error && <div className="error">HTTP Error: {status.http.Error}</div>}
                        {status?.voice.Error && <div className="error">Voice Error: {status.voice.Error}</div>}
                        {status?.control.Error && <div className="error">Control Error: {status.control.Error}</div>}
                    </div>
                )}
            </div>
        </div>
    );
};

export default ServerControls;