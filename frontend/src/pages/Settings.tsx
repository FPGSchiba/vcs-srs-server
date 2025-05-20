import React, { useEffect } from 'react';
import { Box, Button, FormControl, FormLabel, Paper, TextField, Typography } from "@mui/material";
import { GetSettings, SaveGeneralSettings, SaveServerSettings } from "../../wailsjs/go/app/App";
import { useForm, Controller } from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";

const settingsSchema = z.object({
    General: z.object({
        MaxRadiosPerUser: z.number().min(1, "Must be at least 1"),
    }),
    Servers: z.object({
        HTTP: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string().min(1, "Required"),
        }),
        Voice: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string().min(1, "Required"),
        }),
        Control: z.object({
            Port: z.number().min(1, "Required"),
            Host: z.string().min(1, "Required"),
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
            // Convert string/number fields as needed
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
            await SaveServerSettings({ ...data.Servers, convertValues: () => {} });
            await fetchSettings();
        } catch (error) {
            console.error('Failed to save settings:', error);
        }
    };

    useEffect(() => {
        fetchSettings();
        const interval = setInterval(fetchSettings, 3000);
        return () => clearInterval(interval);
    }, []);

    return (
        <Paper className="settings settings-paper">
            <form onSubmit={handleSubmit(onSubmit)} style={{ height: "100%" }}>
                <Box className="settings settings-content">
                    <Box className="settings settings-general settings-general-wrapper">
                        <Typography className="settings settings-general settings-general-title" variant="h4">General</Typography>
                        <FormControl className="settings settings-general settings-general-control" component="fieldset" >
                            <FormLabel className="settings settings-general settings-general-label" id="demo-simple-select-label">Max Number of Radios per User</FormLabel>
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
                <Button className="settings settings-save" variant="contained" type="submit">Save</Button>
            </form>
        </Paper>
    );
}

export default SettingsPage;