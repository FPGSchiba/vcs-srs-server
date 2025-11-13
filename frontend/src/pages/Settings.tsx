import React, { useEffect } from 'react';
import { Box, Button, FormControl, FormLabel, Paper, TextField, Typography } from "@mui/material";
import { GetSettings, SaveGeneralSettings, SaveServerSettings } from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/settingsservice";
import { useForm, Controller } from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import { SettingsState } from "../../bindings/github.com/FPGSchiba/vcs-srs-server/state";
import { Events } from "@wailsio/runtime";
import { WailsEvent } from "@wailsio/runtime/types/events";

const settingsSchema = z.object({
    General: z.object({
        MaxRadiosPerUser: z.number().min(1, "Must be at least 1"),
    }),
    Servers: z.object({
        HTTP: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string(),
        }),
        Voice: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string(),
        }),
        Control: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string(),
        }),
    }),
});

type SettingsFormType = z.infer<typeof settingsSchema>;

function SettingsPage() {
    const { control, handleSubmit, reset } = useForm<SettingsFormType>({
        resolver: zodResolver(settingsSchema),
        defaultValues: {
            General: { MaxRadiosPerUser: 1 },
            Servers: {
                HTTP: { Port: 80, Host: "" },
                Voice: { Port: 5002, Host: "" },
                Control: { Port: 5002, Host: "" },
            },
        },
    });

    const fetchSettings = async () => {
        try {
            const newSettings = await GetSettings();
            if (!newSettings) {
                console.error('No settings found');
                return;
            }
            reset({
                General: {
                    MaxRadiosPerUser: Number(newSettings.General.MaxRadiosPerUser) || 1,
                },
                Servers: {
                    HTTP: {
                        Port: Number(newSettings.Servers.HTTP.Port) || 80,
                        Host: newSettings.Servers.HTTP.Host ?? "",
                    },
                    Voice: {
                        Port: Number(newSettings.Servers.Voice.Port) || 5002,
                        Host: newSettings.Servers.Voice.Host ?? "",
                    },
                    Control: {
                        Port: Number(newSettings.Servers.Control.Port) || 5002,
                        Host: newSettings.Servers.Control.Host ?? "",
                    },
                },
            });
        } catch (error) {
            console.error('Failed to fetch settings:', error);
        }
    };

    const onSubmit = async (data: SettingsFormType) => {
        try {
            await SaveGeneralSettings(data.General);
            await SaveServerSettings({ ...data.Servers });
            await fetchSettings();
        } catch (error) {
            console.error('Failed to save settings:', error);
        }
    };

    const handleSettingsChange = async (event: WailsEvent) => {
        const settings = event.data as SettingsState;
        reset({
            General: {
                MaxRadiosPerUser: Number(settings.General.MaxRadiosPerUser) || 1,
            },
            Servers: {
                HTTP: {
                    Port: Number(settings.Servers.HTTP.Port) || 80,
                    Host: settings.Servers.HTTP.Host ?? "",
                },
                Voice: {
                    Port: Number(settings.Servers.Voice.Port) || 5002,
                    Host: settings.Servers.Voice.Host ?? "",
                },
                Control: {
                    Port: Number(settings.Servers.Control.Port) || 5002,
                    Host: settings.Servers.Control.Host ?? "",
                },
            },
        })
    }

    useEffect(() => {
        fetchSettings();
        Events.On("settings/changed", handleSettingsChange);
    }, []);

    return (
        <form onSubmit={handleSubmit(onSubmit)} className="settings settings-form">
            <Paper className="settings settings-paper">
                <Box className="settings settings-content">
                    <Box className="settings settings-general settings-general-wrapper">
                        <Typography className="settings settings-general settings-general-title" variant="h4">General</Typography>
                        <FormControl className="settings settings-general settings-general-control" component="fieldset" >
                            <FormLabel className="settings settings-general settings-general-label">Max Number of Radios per User</FormLabel>
                            <Controller
                                name="General.MaxRadiosPerUser"
                                control={control}
                                render={({ field, fieldState }) => (
                                    <TextField
                                        {...field}
                                        type="number"
                                        variant="outlined"
                                        error={!!fieldState.error}
                                        helperText={fieldState.error?.message}
                                        onChange={e => field.onChange(e.target.value === "" ? "" : Number(e.target.value))}
                                    />
                                )}
                            />
                        </FormControl>
                    </Box>
                    <Box className="settings settings-server settings-server-wrapper">
                        <Typography className="settings settings-server settings-server-title" variant="h4">Servers</Typography>
                        <Box>
                            <Typography className="" variant="h6" >HTTP Server</Typography>
                            <FormControl className="settings settings-server settings-server-control" component="fieldset" >
                                <FormLabel className="settings settings-server settings-server-label" >HTTP Server Port</FormLabel>
                                <Controller
                                    name="Servers.HTTP.Port"
                                    control={control}
                                    render={({ field, fieldState }) => (
                                        <TextField
                                            {...field}
                                            type="number"
                                            variant="outlined"
                                            error={!!fieldState.error}
                                            helperText={fieldState.error?.message}
                                            onChange={e => field.onChange(e.target.value === "" ? "" : Number(e.target.value))}
                                        />
                                    )}
                                />
                            </FormControl>
                            <FormControl className="settings settings-server settings-server-control" component="fieldset" >
                                <FormLabel className="settings settings-server settings-server-label" >HTTP Server Host</FormLabel>
                                <Controller
                                    name="Servers.HTTP.Host"
                                    control={control}
                                    render={({ field, fieldState }) => (
                                        <TextField
                                            {...field}
                                            variant="outlined"
                                            error={!!fieldState.error}
                                            helperText={fieldState.error?.message}
                                        />
                                    )}
                                />
                            </FormControl>
                        </Box>
                        <Box>
                            <Typography className="" variant="h6" >Voice Server</Typography>
                            <FormControl className="settings settings-server settings-server-control" component="fieldset" >
                                <FormLabel className="settings settings-server settings-server-label" >Voice Server Port</FormLabel>
                                <Controller
                                    name="Servers.Voice.Port"
                                    control={control}
                                    render={({ field, fieldState }) => (
                                        <TextField
                                            {...field}
                                            type="number"
                                            variant="outlined"
                                            error={!!fieldState.error}
                                            helperText={fieldState.error?.message}
                                            onChange={e => field.onChange(e.target.value === "" ? "" : Number(e.target.value))}
                                        />
                                    )}
                                />
                            </FormControl>
                            <FormControl className="settings settings-server settings-server-control" component="fieldset" >
                                <FormLabel className="settings settings-server settings-server-label">Voice Server Host</FormLabel>
                                <Controller
                                    name="Servers.Voice.Host"
                                    control={control}
                                    render={({ field, fieldState }) => (
                                        <TextField
                                            {...field}
                                            variant="outlined"
                                            error={!!fieldState.error}
                                            helperText={fieldState.error?.message}
                                        />
                                    )}
                                />
                            </FormControl>
                        </Box>
                        <Box>
                            <Typography className="" variant="h6" >Control Server</Typography>
                            <FormControl className="settings settings-server settings-server-control" component="fieldset" >
                                <FormLabel className="settings settings-server settings-server-label">Control Server Port</FormLabel>
                                <Controller
                                    name="Servers.Control.Port"
                                    control={control}
                                    render={({ field, fieldState }) => (
                                        <TextField
                                            {...field}
                                            type="number"
                                            variant="outlined"
                                            error={!!fieldState.error}
                                            helperText={fieldState.error?.message}
                                            onChange={e => field.onChange(e.target.value === "" ? "" : Number(e.target.value))}
                                        />
                                    )}
                                />
                            </FormControl>
                            <FormControl className="settings settings-server settings-server-control" component="fieldset" >
                                <FormLabel className="settings settings-server settings-server-label">Control Server Host</FormLabel>
                                <Controller
                                    name="Servers.Control.Host"
                                    control={control}
                                    render={({ field, fieldState }) => (
                                        <TextField
                                            {...field}
                                            variant="outlined"
                                            error={!!fieldState.error}
                                            helperText={fieldState.error?.message}
                                        />
                                    )}
                                />
                            </FormControl>
                        </Box>
                    </Box>
                </Box>
            </Paper>
            <Button className="settings settings-save" variant="contained" type="submit">Save</Button>
        </form>
    );
}

export default SettingsPage;