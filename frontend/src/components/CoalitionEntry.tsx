import * as React from 'react';
import {Coalition} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/state";
import {Box, Button, Paper, TextField, Typography} from "@mui/material";
import {MuiColorInput} from "mui-color-input";
import {Controller, useForm} from "react-hook-form";
import {zodResolver} from "@hookform/resolvers/zod";
import {z} from "zod";
import {UpdateCoalition} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/coalitionservice";

function CoalitionEntry(props: Readonly<{ coalition: Coalition, openDeleteDialog: () => void }>) {
    const { coalition, openDeleteDialog } = props;

    const coalitionSchema = z.object({
        Description: z.string().min(1, "Description is required"),
        Color: z.string().min(1, "Color is required"),
        Password: z.string().min(1, "Password is required"),
    });

    type CoalitionForm = z.infer<typeof coalitionSchema>;

    const { register, handleSubmit, formState: { errors }, control } = useForm<CoalitionForm>({
        resolver: zodResolver(coalitionSchema),
        defaultValues: {
            Description: coalition.Description,
            Color: coalition.Color,
            Password: coalition.Password,
        },
    });

    const onSubmit = (data: CoalitionForm) => {
        UpdateCoalition({
            ...coalition,
            Description: data.Description,
            Color: data.Color,
            Password: data.Password,
        })
    };

    return (
        <form onSubmit={handleSubmit(onSubmit)}>
            <Paper className="coalitions coalitions-entry coalitions-entry-paper">
                <Box className="coalitions coalitions-entry coalitions-entry-content">
                    <Typography variant={"h5"} className="coalitions coalitions-entry coalitions-entry-name">{coalition.Name}</Typography>
                    <TextField
                        required
                        margin="dense"
                        label="Description"
                        fullWidth
                        variant="outlined"
                        {...register("Description")}
                        error={!!errors.Description}
                        helperText={errors.Description?.message}
                    />
                    <Box className="coalitions coalitions-entry coalitions-entry-header">
                        <Controller
                            name="Color"
                            control={control}
                            render={({ field }) => (
                                <MuiColorInput
                                    required
                                    className="coalitions coalitions-entry coalitions-entry-color"
                                    margin="dense"
                                    format="hex"
                                    label="Color"
                                    variant="outlined"
                                    value={field.value ?? "#ffffff"}
                                    onChange={field.onChange}
                                    onBlur={field.onBlur}
                                    error={!!errors.Color}
                                    helperText={errors.Color?.message}
                                />
                            )}
                        />
                        <TextField
                            required
                            margin="dense"
                            label="Password"
                            variant="outlined"
                            className="coalitions coalitions-entry coalitions-entry-password"
                            {...register("Password")}
                            error={!!errors.Password}
                            helperText={errors.Password?.message}
                        />
                    </Box>
                </Box>
                <Box className="coalitions coalitions-entry coalitions-entry-actions">
                    <Button className="coalitions coalitions-entry coalitions-entry-button" variant="contained" type="submit">Save</Button>
                    <Button className="coalitions coalitions-entry coalitions-entry-button" variant="contained" color="error" onClick={openDeleteDialog}>Delete</Button>
                </Box>
            </Paper>
        </form>
    );
}

export default CoalitionEntry;