import React from "react";
import { useForm, Controller } from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import { Button, DialogActions, DialogContentText, Select, TextField } from "@mui/material";

const frequencySchema = z.object({
    frequencyType: z.enum(["global", "test"], { required_error: "Type is required" }),
    frequency: z
        .number({ invalid_type_error: "Frequency must be a number" })
        .min(0.001, "Minimum is 000.001")
        .max(999.999, "Maximum is 999.999"),
});
type FrequencyFormType = z.infer<typeof frequencySchema>;

function formatFrequencyInput(digits: string) {
    // Always 6 digits, insert dot after 3rd
    const padded = digits.padEnd(6, "0").slice(0, 6);
    return `${padded.slice(0, 3)}.${padded.slice(3, 6)}`;
}

function parseFrequencyInput(formatted: string): number {
    return parseFloat(formatted.replace(",", "."));
}

function FrequencyForm({ onSubmit, onCancel, }: Readonly<{
    onSubmit: (data: FrequencyFormType) => void;
    onCancel: () => void;
}>) {
    const { handleSubmit, control, formState: { errors }, setValue } = useForm<FrequencyFormType>({
        resolver: zodResolver(frequencySchema),
        defaultValues: { frequencyType: "global", frequency: 0.0 },
    });

    // Store only the digits, always 6 chars
    const [digits, setDigits] = React.useState("000000"); // default: 000.000
    const inputRef = React.useRef<HTMLInputElement>(null);

    // Overtype handler (Don't ask me how this works)
    const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
        const pos = inputRef.current?.selectionStart ?? 0;
        // Only allow editing digit positions (0-2, 4-6)
        const digitPositions = [0, 1, 2, 4, 5, 6];
        if (e.key.length === 1 && /\d/.test(e.key) && digitPositions.includes(pos)) {
            e.preventDefault();
            let idx = pos < 3 ? pos : pos - 1; // Map input pos to digits index
            const newDigits =
                digits.slice(0, idx) + e.key + digits.slice(idx + 1, 6);
            setDigits(newDigits);
            setValue("frequency", parseFrequencyInput(formatFrequencyInput(newDigits)));
            // Move cursor right, skip dot
            setTimeout(() => {
                if (inputRef.current) {
                    let nextPos = pos + 1;
                    if (nextPos === 3) nextPos++; // skip dot
                    inputRef.current.setSelectionRange(nextPos, nextPos);
                }
            }, 0);
        } else if (e.key === "Backspace" && digitPositions.includes(pos - 1)) {
            e.preventDefault();
            let idx = pos - 1 < 3 ? pos - 1 : pos - 2;
            const newDigits =
                digits.slice(0, idx) + "0" + digits.slice(idx + 1, 6);
            setDigits(newDigits);
            setValue("frequency", parseFrequencyInput(formatFrequencyInput(newDigits)));
            setTimeout(() => {
                if (inputRef.current) {
                    let prevPos = pos - 1;
                    if (prevPos === 3) prevPos--; // skip dot
                    inputRef.current.setSelectionRange(prevPos, prevPos);
                }
            }, 0);
        } else if (
            // Prevent typing at dot position
            (pos === 3 && e.key.length === 1 && /\d/.test(e.key)) ||
            // Prevent left/right arrow from landing on dot
            (e.key === "ArrowLeft" && pos === 4) ||
            (e.key === "ArrowRight" && pos === 3)
        ) {
            e.preventDefault();
            setTimeout(() => {
                if (inputRef.current) {
                    let newPos = e.key === "ArrowLeft" ? 2 : 4;
                    inputRef.current.setSelectionRange(newPos, newPos);
                }
            }, 0);
        }
    };

    // Keep cursor from landing on dot
    const handleSelect = (e: React.SyntheticEvent<HTMLInputElement>) => {
        const pos = e.currentTarget.selectionStart ?? 0;
        if (pos === 3) {
            setTimeout(() => {
                if (inputRef.current) {
                    inputRef.current.setSelectionRange(4, 4);
                }
            }, 0);
        }
    };

    return (
        <form
            onSubmit={handleSubmit((data) => {
                onSubmit({
                    ...data,
                    frequency: parseFrequencyInput(formatFrequencyInput(digits)),
                });
            })}
            className="frequencies frequencies-create frequencies-create-form"
        >
            <Controller
                name="frequencyType"
                control={control}
                render={({ field }) => (
                    <Select
                        {...field}
                        autoFocus
                        variant="outlined"
                        fullWidth
                        error={!!errors.frequencyType}
                        native
                        label="Frequency Type"
                        className="frequencies frequencies-create frequencies-create-type"
                    >
                        <option value="global">Global</option>
                        <option value="test">Test</option>
                    </Select>
                )}
            />
            {errors.frequencyType && (
                <DialogContentText color="error">
                    {errors.frequencyType.message}
                </DialogContentText>
            )}
            <Controller
                name="frequency"
                control={control}
                render={() => (
                    <TextField
                        inputRef={inputRef}
                        value={formatFrequencyInput(digits)}
                        margin="dense"
                        className="frequencies frequencies-create frequencies-create-frequency"
                        id="frequency"
                        label="Frequency"
                        type="text"
                        fullWidth
                        variant="outlined"
                        error={!!errors.frequency}
                        helperText={errors.frequency?.message || "Format: 123.123"}
                        inputProps={{
                            maxLength: 7,
                            inputMode: "numeric",
                            pattern: "\\d{3}[.,]\\d{3}",
                            style: { fontFamily: "monospace" },
                        }}
                        onKeyDown={handleKeyDown}
                        onSelect={handleSelect}
                        onChange={() => {}} // prevent React warning
                    />
                )}
            />
            <DialogContentText className="frequencies frequencies-create frequencies-create-text">
                Add a new frequency to the list.
            </DialogContentText>
            <DialogActions className="frequencies frequencies-create frequencies-create-actions">
                <Button
                    onClick={onCancel}
                    variant="contained"
                    className="frequencies frequencies-create frequencies-create-action"
                >
                    Cancel
                </Button>
                <Button
                    type="submit"
                    variant="contained"
                    autoFocus
                    color="secondary"
                    className="frequencies frequencies-create frequencies-create-action"
                >
                    Add
                </Button>
            </DialogActions>
        </form>
    );
}

export default FrequencyForm;