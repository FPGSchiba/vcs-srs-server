import React from "react";
import {IconButton} from "@mui/material";
import CloseIcon from '@mui/icons-material/Close';
import RemoveIcon from '@mui/icons-material/Remove';
import VCSIcon from "../assets/images/logo-universal.png";
import {Window, Application} from "@wailsio/runtime";

function Header() {
    return (
        <header className="header header-container">
            <div className="header header-icon header-icon-container">
                <img src={VCSIcon} alt="VCS Server icon" className="header header-icon header-icon-image" />
            </div>
            <div className="header header-action header-action-container">
                <IconButton onClick={Window.Minimise} className="header header-action header-action-minimize"><RemoveIcon /></IconButton>
                <IconButton onClick={Application.Quit} className="header header-action header-action-close"><CloseIcon /></IconButton>
            </div>
        </header>
    );
}

export default Header;