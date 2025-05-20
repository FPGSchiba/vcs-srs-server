import * as React from 'react';
import Box from '@mui/material/Box';
import Tab from '@mui/material/Tab';
import TabContext from '@mui/lab/TabContext';
import TabList from '@mui/lab/TabList';
import TabPanel from '@mui/lab/TabPanel';
import SettingsPage from "../pages/Settings";
import CoalitionsPage from "../pages/Coalitions";
import ClientListPage from "../pages/ClientList";
import BanManagement from "../pages/BanManagement";
import FrequencyPage from "../pages/FrequencyPage";


function ContentWrapper() {
    const [value, setValue] = React.useState('1');

    const handleChange = (event: React.SyntheticEvent, newValue: string) => {
        setValue(newValue);
    };

    return (
        <Box className="content content-wrapper" sx={{ typography: 'body1' }}>
            <TabContext value={value}>
                <Box className="nav nav-wrapper">
                    <TabList onChange={handleChange}>
                        <Tab className="nav nav-tab nav-tab-button" label="Settings" value="1" />
                        <Tab className="nav nav-tab nav-tab-button" label="Coalitions" value="2" />
                        <Tab className="nav nav-tab nav-tab-button" label="Clients" value="3" />
                        <Tab className="nav nav-tab nav-tab-button" label="Banned Clients" value="4" />
                        <Tab className="nav nav-tab nav-tab-button" label="Frequencies" value="5" />
                    </TabList>
                </Box>
                <TabPanel className="nav nav-tab nav-tab-container" value="1" >
                    <SettingsPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="2">
                    <CoalitionsPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="3">
                    <ClientListPage />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="4">
                    <BanManagement />
                </TabPanel>
                <TabPanel className="nav nav-tab nav-tab-container" value="5">
                    <FrequencyPage />
                </TabPanel>
            </TabContext>
        </Box>
    );
}

export default ContentWrapper;