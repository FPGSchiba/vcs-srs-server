import React from 'react'
import {createRoot} from 'react-dom/client'
import './assets/style/index.scss'
import App from './App'
import {BrowserRouter} from "react-router";
import {ThemeProvider} from "@mui/material";
import { theme } from "./theme";

const container = document.getElementById('root')

const root = createRoot(container!)

root.render(
    <React.StrictMode>
        <BrowserRouter>
            <ThemeProvider theme={theme}>
                <App/>
            </ThemeProvider>
        </BrowserRouter>
    </React.StrictMode>
)
