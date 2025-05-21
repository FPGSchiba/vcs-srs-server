import * as React from 'react';
import {Alert} from "@mui/material";
import Typography from "@mui/material/Typography";
import IconButton from "@mui/material/IconButton";
import CloseIcon from '@mui/icons-material/Close';
import { events } from "../../wailsjs/go/models";
import {useEffect} from "react";

export function ClosingAlert(props: Readonly<{ notification: events.Notification, closeNotification: (id: string) => void }>) {
    const {notification, closeNotification} = props;

    useEffect(() => {
        setTimeout(() => {
            closeNotification(notification.id);
        }, 8000);
    }, []);

    return (
        <Alert className="notification notification-paper" severity={notification.level as 'error' | 'warning' | 'info' | 'success'} key={notification.id} style={{overflow: "auto"}}>
            <div className="notification notification-body notification-body-wrapper">
                <Typography className="notification notification-body notification-body-header" variant="h5">{notification.title}</Typography>
                <Typography className="notification notification-body notification-body-message" variant="body1">{notification.message}</Typography>
            </div>
            <IconButton
                size="small"
                aria-label="close"
                color="inherit"
                className="notification notification-button"
                onClick={() => closeNotification(notification.id)}
            >
                <CloseIcon fontSize="small"/>
            </IconButton>
        </Alert>
    )
}