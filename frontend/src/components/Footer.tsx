import { Typography } from "@mui/material";
import React, {useEffect, useState} from "react";
import {GetServerVersion} from "../../bindings/github.com/FPGSchiba/vcs-srs-server/services/controlservice";

function Footer() {
    const [version, setVersion] = useState("loading...");

    useEffect(() => {
        GetServerVersion().then(newVersion => {
            setVersion(newVersion);
        });
    })

    return (
        <footer className="footer footer-container">
            <Typography className="footer footer-server" variant="body2" color="textSecondary">VCS Server</Typography>
            <Typography className="footer footer-version" variant="body2" color="textSecondary">{version}</Typography>
        </footer>
    );
}

export default Footer;