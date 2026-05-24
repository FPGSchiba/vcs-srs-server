import React, { useState } from "react";
import ServerControls from "./components/ServerControl";
import Header from "./components/Header";
import Footer from "./components/Footer";
import ContentWrapper from "./components/ContentWrapper";
import MessageWrapper from "./components/MessageWrapper";
import LogsDialog from "./components/LogsDialog";

function App() {
    const [logsOpen, setLogsOpen] = useState(false);

    return (
        <div id="App">
            <Header onOpenLogs={() => setLogsOpen(true)} />
            <MessageWrapper />
            <ServerControls />
            <ContentWrapper />
            <Footer />
            <LogsDialog open={logsOpen} onClose={() => setLogsOpen(false)} />
        </div>
    )
}

export default App
