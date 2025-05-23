import {Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Paper, TextField, Typography} from "@mui/material";
import React, {useEffect} from "react";
import CoalitionEntry from "../components/CoalitionEntry";
import {Controller, useForm} from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import {MuiColorInput} from "mui-color-input";
import {AddCoalition, GetCoalitions, Notify, RemoveCoalition} from "../../wailsjs/go/app/App";
import {EventsOn} from "../../wailsjs/runtime";
import {events, state} from "../../wailsjs/go/models";

const coalitionSchema = z.object({
    Name: z.string().min(1, "Name is required"),
    Description: z.string().min(1, "Description is required"),
    Color: z.string().min(1, "Color is required"),
    Password: z.string().min(1, "Password is required"),
});
type CoalitionForm = z.infer<typeof coalitionSchema>;

// Add this component in the same file or import it from another file
function CoalitionFormComponent({onSubmit, onCancel }: { onSubmit: (data: CoalitionForm) => void; onCancel: () => void; }) {
    const { register, handleSubmit, formState: { errors }, control } = useForm<CoalitionForm>({
        resolver: zodResolver(coalitionSchema),
    });

    return (
        <form className="coalitions coalitions-create coalitions-create-form" onSubmit={handleSubmit(onSubmit)}>
            <div className="coalitions coalitions-create coalitions-create-wrapper">
                <TextField
                    autoFocus
                    required
                    margin="dense"
                    label="Name"
                    variant="outlined"
                    {...register("Name")}
                    error={!!errors.Name}
                    helperText={errors.Name?.message}
                />
                <Controller
                    name="Color"
                    control={control}
                    render={({ field }) => (
                        <MuiColorInput
                            required
                            className="coalitions coalitions-create coalitions-create-color"
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
            </div>
            <TextField
                required
                margin="dense"
                label="Password"
                fullWidth
                variant="outlined"
                {...register("Password")}
                error={!!errors.Password}
                helperText={errors.Password?.message}
            />
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
            <DialogActions sx={{ mt: 2 }}>
                <Button onClick={onCancel} variant="contained" color="error">Cancel</Button>
                <Button type="submit" variant="contained">Create</Button>
            </DialogActions>
        </form>
    );
}

function CoalitionsPage() {
    const [deleteOpen, setDeleteOpen] = React.useState(false);
    const [deleteItem, setDeleteFor] = React.useState<state.Coalition | null>(null);
    const [createOpen, setCreateOpen] = React.useState(false);
    const [coalitions, setCoalitions] = React.useState<state.Coalition[]>([]);

    const fetchCoalitions = async () => {
        const data = await GetCoalitions();
        setCoalitions(data);
    }

    useEffect(() => {
        EventsOn("settings/coalitions/changed", (data: state.Coalition[]) => {
            setCoalitions(data);
        });
        if (coalitions.length === 0) {
            fetchCoalitions();
        }
    }, []);

    return (
        <>
            <Paper className="coalitions coalitions-paper">
                <Box className="coalitions coalitions-list">
                    {coalitions.map((coalition, index) => (
                        <CoalitionEntry key={index} coalition={coalition} openDeleteDialog={() => {
                            setDeleteFor(coalition);
                            setDeleteOpen(true);
                        }} />
                    ))}
                </Box>
            </Paper>
            <Button onClick={() => {setCreateOpen(true)}} className="coalitions coalitions-add" variant="contained">Add Coalition</Button>
            <Dialog
                open={deleteOpen}
                onClose={() => {setDeleteOpen(false)}}
            >
                <DialogTitle>
                    Delete Coalition
                </DialogTitle>
                <DialogContent>
                    <DialogContentText>
                        Are you sure you want to delete this coalition? This action cannot be undone.
                    </DialogContentText>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => {setDeleteOpen(false)}} variant="contained">Cancel</Button>
                    <Button onClick={() => {
                        if (deleteItem) {
                            RemoveCoalition(deleteItem)
                            setDeleteOpen(false);
                        } else {
                            Notify(new events.Notification({
                                Title: "No coalition selected",
                                Message: `No coalition selected for deletion`,
                                Type: "error",
                            }));
                            setDeleteOpen(false);
                        }
                    }} variant="contained" autoFocus color="error">Delete</Button>
                </DialogActions>
            </Dialog>
            <Dialog
                open={createOpen}
                onClose={() => { setCreateOpen(false); }}
            >
                <DialogTitle>Add Coalition</DialogTitle>
                <DialogContent>
                    <DialogContentText sx={{mb: 2}}>
                        Please enter the coalition details below.
                    </DialogContentText>
                    <CoalitionFormComponent
                        onSubmit={(data: CoalitionForm) => {
                            AddCoalition(data);
                            setCreateOpen(false);
                        }}
                        onCancel={() => setCreateOpen(false)}
                    />
                </DialogContent>
            </Dialog>
        </>
    )
}

export default CoalitionsPage;