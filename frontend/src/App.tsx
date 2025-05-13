import ServerControls from "./components/ServerControl";
import { Routes, Route } from "react-router";
import SettingsPage from "./pages/Settings";
import CoalitionsPage from "./pages/Coalitions";
import ClientListPage from "./pages/ClientList";
import Header from "./components/Header";
import Footer from "./components/Footer";
import Navigation from "./components/Navigation";

function App() {
    return (
        <div id="App">
            <Header />
            <ServerControls />
            <Navigation />
            <Routes>
                <Route index element={<SettingsPage />} />
                <Route path="/coalitions" element={<CoalitionsPage />} />
                <Route path="/client-list" element={<ClientListPage />} />
            </Routes>
            <Footer />
        </div>
    )
}

export default App
