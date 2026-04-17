import React from 'react';
import {
    Dialog,
    DialogTitle,
    DialogContent,
    IconButton,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';
import LogsPage from '../pages/Logs';

interface LogsDialogProps {
    open: boolean;
    onClose: () => void;
}

function LogsDialog({ open, onClose }: LogsDialogProps) {
    return (
        <Dialog
            open={open}
            onClose={onClose}
            fullWidth
            maxWidth="lg"
            keepMounted
            PaperProps={{ sx: { height: '80vh' } }}
        >
            <DialogTitle sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', pb: 1 }}>
                Logs
                <IconButton onClick={onClose} size="small"><CloseIcon /></IconButton>
            </DialogTitle>
            <DialogContent sx={{ display: 'flex', flexDirection: 'column', p: 2, overflow: 'hidden' }}>
                <LogsPage />
            </DialogContent>
        </Dialog>
    );
}

export default LogsDialog;
