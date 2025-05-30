import React from "react";
import {
    Box,
    Button,
    Dialog,
    DialogContent,
    DialogTitle,
    IconButton,
    List,
    ListItem,
    ListItemIcon,
    ListItemText,
    ListSubheader,
    Paper
} from "@mui/material";
import PodcastsIcon from '@mui/icons-material/Podcasts';
import {GetSettings, SaveFrequencySettings} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice";
import {Events} from "@wailsio/runtime";
import CloseIcon from '@mui/icons-material/Close';
import {SettingsState, FrequencySettings} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/state";
import FrequencyForm from "../components/FrequencyForm";
import {WailsEvent} from "@wailsio/runtime/types/events";

function formatFrequencyNumber(num: number): string {
    // Convert to string with 3 decimals, remove dot, pad to 6 digits
    const digits = (Math.round(num * 1000)).toString().padStart(6, "0");
    return `${digits.slice(0, 3)}.${digits.slice(3, 6)}`;
}

function FrequencyPage() {
    const [globalFrequencies, setGlobalFrequencies] = React.useState<number[]>([]);
    const [testFrequencies, setTestFrequencies] = React.useState<number[]>([]);
    const [open, setOpen] = React.useState(false);

    const fetchFrequencies = async () => {
        const settings = await GetSettings();
        if (!settings) {
            console.error("Frequencies settings not found");
            return;
        }
        setGlobalFrequencies(settings.Frequencies.GlobalFrequencies);
        setTestFrequencies(settings.Frequencies.TestFrequencies);
    }

    const handleSave = async () => {
        await SaveFrequencySettings(new FrequencySettings({
            GlobalFrequencies: globalFrequencies,
            TestFrequencies: testFrequencies,
        }));
    }

    React.useEffect(() => {
        fetchFrequencies();
        Events.On("settings/changed", (event: WailsEvent) => {
            const settings = event.data[0] as SettingsState;
            if (settings.Frequencies) {
                setGlobalFrequencies(settings.Frequencies.GlobalFrequencies);
                setTestFrequencies(settings.Frequencies.TestFrequencies);
            }
        })
    }, []);

    return (
        <>
            <Paper className="frequencies frequencies-paper">
                <List
                    subheader={<ListSubheader>Global Frequencies</ListSubheader>}
                    className="frequencies frequencies-list frequencies-list-global"
                >
                    {globalFrequencies.map((frequency, index) => (
                        <ListItem key={index} className="frequencies frequencies-list frequencies-list-item">
                            <ListItemIcon className="frequencies frequencies-list frequencies-list-icon">
                                <PodcastsIcon color="secondary" />
                            </ListItemIcon>
                            <ListItemText primary={formatFrequencyNumber(frequency)} className="frequencies frequencies-list frequencies-list-name" />
                            <IconButton className="frequencies frequencies-list frequencies-list-close" onClick={() => {
                                const newFrequencies = [...globalFrequencies];
                                newFrequencies.splice(index, 1);
                                setGlobalFrequencies(newFrequencies);
                            }}>
                                <CloseIcon />
                            </IconButton>
                        </ListItem>
                    ))}
                </List>
                <List
                    subheader={<ListSubheader>Test Frequencies</ListSubheader>}
                    className="frequencies frequencies-list frequencies-list-test"
                >
                    {testFrequencies.map((frequency, index) => (
                        <ListItem key={index} className="frequencies frequencies-list frequencies-list-item">
                            <ListItemIcon className="frequencies frequencies-list frequencies-list-icon">
                                <PodcastsIcon color="secondary" />
                            </ListItemIcon>
                            <ListItemText primary={formatFrequencyNumber(frequency)} className="frequencies frequencies-list frequencies-list-name" />
                            <IconButton className="frequencies frequencies-list frequencies-list-close" onClick={() => {
                                const newFrequencies = [...testFrequencies];
                                newFrequencies.splice(index, 1);
                                setTestFrequencies(newFrequencies);
                            }}>
                                <CloseIcon />
                            </IconButton>
                        </ListItem>
                    ))}

                </List>
            </Paper>
            <Box className="frequencies frequencies-actions">
                <Button variant="contained" color="secondary" className="frequencies frequencies-action" onClick={() => {setOpen(true)}}>Add Frequency</Button>
                <Button variant="contained" className="frequencies frequencies-action" onClick={handleSave}>Save</Button>
            </Box>
            <Dialog
                open={open}
                onClose={() => { setOpen(false); }}
            >
                <DialogTitle>
                    Add Frequency
                </DialogTitle>
                <DialogContent>
                    <FrequencyForm
                        onSubmit={({ frequencyType, frequency }) => {
                            if (frequencyType === "global") {
                                setGlobalFrequencies([...globalFrequencies, frequency]);
                            } else {
                                setTestFrequencies([...testFrequencies, frequency]);
                            }
                            setOpen(false);
                        }}
                        onCancel={() => setOpen(false)}
                    />
                </DialogContent>
            </Dialog>
        </>
    )
}

export default FrequencyPage;