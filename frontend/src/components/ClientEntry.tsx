import * as React from 'react';
import {events, state} from "../../wailsjs/go/models";
import {Box, Button, Paper, Typography} from "@mui/material";
import CircleIcon from "@mui/icons-material/Circle";
import {GetCoalitionByName, IsClientMuted, MuteClient, Notify, UnmuteClient} from "../../wailsjs/go/app/App";
import {EventsOn} from "../../wailsjs/runtime";

function ClientEntry(props: Readonly<{ client: state.ClientState, clientId: string, handleBan: (clientId: string) => void, handleKick: (clientId: string) => void }>) {
    const { client, clientId, handleBan, handleKick } = props;
    const [coalition, setCoalition] = React.useState<state.Coalition | null>(null);
    const [muted, setMuted] = React.useState<boolean>(false);

    const fetchRadioClient = async () => {
        const muted = await IsClientMuted(clientId);
        setMuted(muted);
    }

    React.useEffect(() => {
        if (client.Coalition) {
            GetCoalitionByName(client.Coalition).then((coalition?: state.Coalition) => {
                if (coalition) {
                    setCoalition(coalition);
                } else {
                    Notify(new events.Notification({
                        Title: "Client Coalition not found",
                        Message: `Client ${client.Name} has no valid coalition`,
                        Type: "warning",
                    }));
                }
            });
        }
        fetchRadioClient()
        EventsOn("clients/radio/changed", (clients: Record<string, state.RadioState>) => {
            if (clients[clientId]) {
                setMuted(clients[clientId].Muted);
            }
        })
    }, [client]);

    return (
        <Paper className="clients clients-entry clients-entry-paper">
            <Typography className="clients clients-entry clients-entry-name" variant="body1">[{client.UnitId}] {client.Name}</Typography>
            <Box className="clients clients-entry clients-entry-coalition wrapper">
                <CircleIcon className="clients clients-entry clients-entry-coalition circle" sx={{ color: coalition?.Color }} />
                <Typography className="clients clients-entry clients-entry-coalition name" variant="body1">{coalition?.Name}</Typography>
            </Box>

            <Box className="clients clients-entry clients-entry-actions">
                <Button variant="contained" className="clients clients-entry clients-entry-action" onClick={() => {handleKick(clientId)}}>Kick</Button>
                <Button variant="contained" className="clients clients-entry clients-entry-action" onClick={() => {handleBan(clientId)}}>Ban</Button>
                <Button variant="contained" color={muted ? "primary" : "error"} className="clients clients-entry clients-entry-action" onClick={() => {
                    if (muted) {
                        UnmuteClient(clientId);
                    } else {
                        MuteClient(clientId);
                    }
                }}>{muted ? "Unmute" : "Mute"}</Button>
            </Box>
        </Paper>
    );
}

export default ClientEntry;