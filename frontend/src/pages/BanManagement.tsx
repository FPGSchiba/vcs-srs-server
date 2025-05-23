import React from "react";
import {Box, Button, Paper, Typography} from "@mui/material";
import {state} from "../../wailsjs/go/models";
import {GetBannedClients, UnbanClient} from "../../wailsjs/go/app/App";
import {EventsOn} from "../../wailsjs/runtime";

function BanManagement() {
    const [bannedClients, setBannedClients] = React.useState<state.BannedClient[]>([]);

    const fetchBannedClients = async () => {
        const bannedClients = await GetBannedClients();
        setBannedClients(bannedClients);
    }

    React.useEffect(() => {
        fetchBannedClients();
        EventsOn("clients/banned/changed", (bannedClients: state.BannedClient[]) => {
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