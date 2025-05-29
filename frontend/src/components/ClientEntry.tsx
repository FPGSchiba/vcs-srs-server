import * as React from 'react';
import {ClientState, RadioState, Coalition} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/state";
import {Notification} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/events";
import {Box, Button, Paper, Typography} from "@mui/material";
import CircleIcon from "@mui/icons-material/Circle";
import {GetCoalitionByName,} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice";
import {Notify} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/notificationservice";
import {IsClientMuted, MuteClient, UnmuteClient} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice"
import {Events} from "@wailsio/runtime";

function ClientEntry(props: Readonly<{ client: ClientState, clientId: string, handleBan: (clientId: string) => void, handleKick: (clientId: string) => void }>) {
    const { client, clientId, handleBan, handleKick } = props;
    const [coalition, setCoalition] = React.useState<Coalition | null>(null);
    const [muted, setMuted] = React.useState<boolean>(false);

    const fetchRadioClient = async () => {
        const muted = await IsClientMuted(clientId);
        setMuted(muted);
    }

    React.useEffect(() => {
        if (client.Coalition) {
            GetCoalitionByName(client.Coalition).then((coalition) => {
                if (coalition) {
                    setCoalition(coalition);
                } else {
                    Notify(new Notification({
                        title: "Client Coalition not found",
                        message: `Client ${client.Name} has no valid coalition`,
                        level: "warning",
                    }));
                }
            });
        }
        fetchRadioClient()
        Events.On("clients/radio/changed", (event) => {
            const clients = event.data[0] as Record<string, RadioState>;
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