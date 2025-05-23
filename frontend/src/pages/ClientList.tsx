import React from "react";
import {Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Paper, TextField} from "@mui/material";
import {events, state} from "../../wailsjs/go/models";
import {BanClient, GetClients, KickClient, Notify} from "../../wailsjs/go/app/App";
import {EventsOn} from "../../wailsjs/runtime";
import ClientEntry from "../components/ClientEntry";

function ClientListPage() {
    const [clients, setClients] = React.useState<Record<string, state.ClientState> | null>(null);
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
        EventsOn("clients/changed", (clients: Record<string, state.ClientState>) => {
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
                            Notify(new events.Notification({
                                Title: "No client selected",
                                Message: `No client selected for ban`,
                                Type: "error",
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
                            Notify(new events.Notification({
                                Title: "No client selected",
                                Message: `No client selected for kick`,
                                Type: "error",
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