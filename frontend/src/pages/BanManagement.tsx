import React from "react";
import {Box, Button, Paper, Typography} from "@mui/material";
import {BannedClient} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/state";
import {GetBannedClients, UnbanClient} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice";
import {Events} from "@wailsio/runtime";

function BanManagement() {
    const [bannedClients, setBannedClients] = React.useState<BannedClient[]>([]);

    const fetchBannedClients = async () => {
        const bannedClients = await GetBannedClients();
        setBannedClients(bannedClients);
    }

    React.useEffect(() => {
        fetchBannedClients();
        Events.On("clients/banned/changed", (event) => {
            const bannedClients = event.data[0] as BannedClient[]
            setBannedClients(bannedClients);
        });
    }, []);

    return (
        <Paper className="ban ban-paper">
            <Box className="ban ban-content">
                {bannedClients.map((client, index) => (
                    <Paper key={index} className="ban ban-entry ban-entry-paper">
                        <Box className="ban ban-entry ban-entry-content">
                            <Typography variant="h5" className="ban ban-entry ban-entry-name" fontWeight="bold">{client.name}</Typography>
                            <Typography variant="body1" className="ban ban-entry ban-entry-reason"><strong>Banned for</strong>: {client.reason}</Typography>
                            <Typography variant="body1" className="ban ban-entry ban-entry-ip"><strong>Blocked IP-Address</strong>: {client.ip_address}</Typography>
                        </Box>
                        <Box className="ban ban-entry ban-entry-actions">
                            <Button variant="contained" className="ban ban-entry ban-entry-action" onClick={() => {UnbanClient(client.id)}} >Unban</Button>
                        </Box>
                    </Paper>
                ))}
            </Box>
        </Paper>
    )
}

export default BanManagement;