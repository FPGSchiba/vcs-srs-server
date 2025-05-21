import * as React from 'react';
import {ClosingAlert} from "./ClosingAlert";
import { Snackbar } from '@mui/material';
import { events } from "../../wailsjs/go/models";
import {EventsOn} from "../../wailsjs/runtime";
import {useEffect} from "react";

function MessageWrapper() {
    const [notifications, setNotifications] = React.useState<{ notification: events.Notification, timestamp: Date }[]>([]);

    useEffect(() => {
        EventsOn("notification", (notification: events.Notification) => {
            setNotifications((prev) => {
                if (prev.findIndex((n) => n.notification.id === notification.id) === -1) {
                    return [...prev, {notification, timestamp: new Date()}];
                }
                return prev;
            });
        });
    }, []);

    return (
        <Snackbar
            anchorOrigin={{ vertical: 'top', horizontal: 'right' }}
            open={notifications.length > 0}
        >
            <div>
                { notifications
                    .slice()
                    .sort((a, b) => b.timestamp.getTime() - a.timestamp.getTime())
                    .map(function (n) {
                        return (
                            <ClosingAlert notification={n.notification} key={n.notification.id} closeNotification={(id: string) => {
                                setNotifications((prev) => {
                                    return prev.filter((n) => n.notification.id != id);
                                });
                            }} />
                        )
                    })}
            </div>
        </Snackbar>
    )
}

export default MessageWrapper;