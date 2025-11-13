import React from "react";
import {Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Paper, TextField} from "@mui/material";
import {ClientState} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/state";
import {Notification} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/events";
import {BanClient, GetClients, KickClient} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/clientservice";
import {Notify} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/notificationservice";
import {Events} from "@wailsio/runtime";
import ClientEntry from "../components/ClientEntry";
import {WailsEvent} from "@wailsio/runtime/types/events";

function ClientListPage() {
    const [clients, setClients] = React.useState<Record<string, ClientState> | null>(null);
    const [banOpen, setBanOpen] = React.useState(false);
    const [banItem, setBanItem] = React.useState<string | null>(null);
    const [banReason, setBanReason] = React.useState<string>("");
    const [kickOpen, setKickOpen] = React.useState(false);
    const [kickItem, setKickItem] = React.useState<string | null>(null);
    const [kickReason, setKickReason] = React.useState<string>("");

    const fetchClients = async () => {
        const clients = await GetClients();
        setClients(clients.Clients);
    }

    const handleBan = (clientId: string) => {
        setBanItem(clientId);
        setBanOpen(true);
    }

    const handleKick = (clientId: string) => {
        setKickItem(clientId);
        setKickOpen(true);
    }

    React.useEffect(() => {
        fetchClients();
        Events.On("clients/changed", (event: WailsEvent) => {
            const clients = event.data as Record<string, ClientState>
            console.log("Received clients changed event:", clients);
            setClients(clients);
        });
    }, []);

    return (
        <>
            <Paper className="clients clients-paper">
                <Box className="clients clients-content">
                    { clients && Object.entries(clients).map(([key, client]) => (
                        <ClientEntry key={key} client={client} clientId={key} handleBan={handleBan} handleKick={handleKick} />
                    ))}
                </Box>
            </Paper>
            <Dialog
                open={banOpen}
                onClose={() => {setBanOpen(false)}}
            >
                <DialogTitle>
                    Delete Coalition
                </DialogTitle>
                <DialogContent>
                    <DialogContentText>
                        You are attempting to ban a client. This action requires a reason. Please enter the reason for the ban.
                    </DialogContentText>
                    <TextField
                        autoFocus
                        margin="dense"
                        id="name"
                        label="Reason for ban"
                        type="text"
                        fullWidth
                        value={banReason}
                        variant="outlined"
                        onChange={(e) => {
                            setBanReason(e.target.value);
                        }}/>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => {setBanOpen(false)}} variant="contained">Cancel</Button>
                    <Button onClick={() => {
                        if (banOpen && banItem) {
                            BanClient(banItem, banReason)
                            setBanOpen(false);
                            setBanItem(null);
                            setBanReason("");
                        } else {
                            Notify(new Notification({
                                title: "No client selected",
                                message: `No client selected for ban`,
                                level: "error",
                            }));
                            setBanItem(null);
                            setBanReason("");
                            setBanOpen(false);
                        }
                    }} variant="contained" autoFocus color="error">Ban</Button>
                </DialogActions>
            </Dialog>
            <Dialog
                open={kickOpen}
                onClose={() => {setKickOpen(false)}}
            >
                <DialogTitle>
                    Delete Coalition
                </DialogTitle>
                <DialogContent>
                    <DialogContentText>
                        You are attempting to kick a client. This action requires a reason. Please enter the reason for the kick.
                    </DialogContentText>
                    <TextField
                        autoFocus
                        margin="dense"
                        id="name"
                        label="Reason for kick"
                        type="text"
                        fullWidth
                        value={kickReason}
                        variant="outlined"
                        onChange={(e) => {
                            setKickReason(e.target.value);
                        }}/>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => {setKickOpen(false)}} variant="contained">Cancel</Button>
                    <Button onClick={() => {
                        if (kickOpen && kickItem) {
                            KickClient(kickItem, kickReason);
                            setKickOpen(false);
                            setKickItem(null);
                            setKickReason("");
                        } else {
                            Notify(new Notification({
                                title: "No client selected",
                                message: `No client selected for kick`,
                                level: "error",
                            }));
                            setKickItem(null);
                            setKickReason("");
                            setKickOpen(false);
                        }
                    }} variant="contained" autoFocus color="error">Kick</Button>
                </DialogActions>
            </Dialog>
        </>
    )
}

export default ClientListPage;