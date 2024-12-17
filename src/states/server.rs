use crate::{states::client::Client, VERSION};
use eframe::egui::{CentralPanel, Id};
use serde::{Deserialize, Serialize};
use std::{collections::HashMap, path::Path};

#[derive(Deserialize, Clone)]
pub struct ServerState {
    pub clients: HashMap<String, Client>,
    pub options: ServerOptions,
    pub version: String,
}

#[derive(Serialize, Deserialize, Clone)]
pub struct ServerOptions {
    pub general: GeneralSettings,
    pub server: ServerSettings,
    pub awacs: AwacsSettings,
}

#[derive(Serialize, Deserialize, Clone)]
pub struct GeneralSettings {
    #[serde(rename = "TRANSMISSION_LOG_ENABLED")]
    pub transmissing_log: bool,
    #[serde(rename = "CLIENT_EXPORT_ENABLED")]
    pub client_export: bool,
    #[serde(rename = "LOTATC_EXPORT_ENABLED")]
    pub client_lotac: bool,
    #[serde(rename = "TEST_FREQUENCIES")]
    pub test_frequencies: Vec<f64>,
    #[serde(rename = "GLOBAL_LOBBY_FREQUENCIES")]
    pub global_lobby_freq: f64,
    #[serde(rename = "EXTERNAL_AWACS_MODE")]
    pub external_awacs_mode: bool,
    #[serde(rename = "COALITION_AUDIO_SECURITY")]
    pub coalition_audio_security: bool,
    #[serde(rename = "SPECTATORS_AUDIO_DISABLED")]
    pub spectators_audio_disabled: bool,
    #[serde(rename = "LOS_ENABLED")]
    pub los_enabled: bool,
    #[serde(rename = "DISTANCE_ENABLED")]
    pub distance_enabled: bool,
    #[serde(rename = "IRL_RADIO_TX")]
    pub irl_radio_tx: bool,
    #[serde(rename = "IRL_RADIO_RX_INTERFERENCE")]
    pub irl_radio_rx_interference: bool,
    #[serde(rename = "RADIO_EXPANSION")]
    pub radio_expansion: bool,
    #[serde(rename = "ALLOW_RADIO_ENCRYPTION")]
    pub allow_radio_encryption: bool,
    #[serde(rename = "STRICT_RADIO_ENCRYPTION")]
    pub strict_radio_encryption: bool,
    #[serde(rename = "SHOW_TUNED_COUNT")]
    pub show_tuned_count: bool,
    #[serde(rename = "RADIO_EFFECT_OVERRIDE")]
    pub radio_effect_override: bool,
    #[serde(rename = "SHOW_TRANSMITTER_NAME")]
    pub show_transmitter_name: bool,
    #[serde(rename = "TRANSMISSION_LOG_RETENTION")]
    pub transmission_log_retention: u32,
    #[serde(rename = "RETRANSMISSION_NODE_LIMIT")]
    pub retransmission_node_limit: u32,
}

#[derive(Serialize, Deserialize, Clone)]
pub struct ServerSettings {
    #[serde(rename = "CLIENT_EXPORT_FILE_PATH")]
    pub client_export_file_path: String,
    #[serde(rename = "SERVER_IP")]
    pub server_ip: String,
    #[serde(rename = "SERVER_PORT")]
    pub server_port: u16,
    #[serde(rename = "UPNP_ENABLED")]
    pub upnp_enabled: bool,
}

#[derive(Serialize, Deserialize, Clone)]
pub struct AwacsSettings {
    #[serde(rename = "EXTERNAL_AWACS_MODE_BLUE_PASSWORD")]
    pub blue_password: String,
    #[serde(rename = "EXTERNAL_AWACS_MODE_RED_PASSWORD")]
    pub red_password: String,
}

impl Default for ServerOptions {
    fn default() -> Self {
        Self {
            general: GeneralSettings::default(),
            server: ServerSettings::default(),
            awacs: AwacsSettings::default(),
        }
    }
}

impl Default for GeneralSettings {
    fn default() -> Self {
        Self {
            transmissing_log: false,
            client_export: false,
            client_lotac: false,
            test_frequencies: vec![247.2, 120.3],
            global_lobby_freq: 248.22,
            external_awacs_mode: false,
            coalition_audio_security: false,
            spectators_audio_disabled: false,
            los_enabled: false,
            distance_enabled: false,
            irl_radio_tx: false,
            irl_radio_rx_interference: false,
            radio_expansion: false,
            allow_radio_encryption: false,
            strict_radio_encryption: false,
            show_tuned_count: true,
            radio_effect_override: false,
            show_transmitter_name: true,
            transmission_log_retention: 2,
            retransmission_node_limit: 0,
        }
    }
}

impl Default for ServerSettings {
    fn default() -> Self {
        Self {
            client_export_file_path: "clients-list.json".to_owned(),
            server_ip: "0.0.0.0".to_owned(),
            server_port: 5002,
            upnp_enabled: true,
        }
    }
}

impl Default for AwacsSettings {
    fn default() -> Self {
        Self {
            blue_password: "blue".to_owned(),
            red_password: "red".to_owned(),
        }
    }
}

impl ServerOptions {
    pub fn to_config_file(&self, filename: &str) -> std::io::Result<()> {
        let config = toml::to_string(self).unwrap();
        std::fs::write(filename, config)
    }

    pub fn from_config_file(filename: &str) -> std::io::Result<Self> {
        if !Path::new(filename).exists() {
            Self::default().to_config_file(filename)?; // Create default config file
        }
        let config = std::fs::read_to_string(filename)?;
        toml::from_str(&config).map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))
    }
}

impl ServerState {
    pub fn new(filename: Option<String>) -> Self {
        if let Some(filename) = filename {
            let settings = ServerOptions::from_config_file(&filename).unwrap();
            return Self {
                clients: HashMap::new(),
                options: settings,
                version: VERSION.to_owned(),
            };
        }
        let settings = ServerOptions::default();
        settings.to_config_file("server.toml").unwrap();
        return Self {
            clients: HashMap::new(),
            options: settings,
            version: VERSION.to_owned(),
        };
    }

    pub fn add_client(&mut self, client: Client) {
        self.clients.insert(client.id.clone(), client);
    }

    pub fn remove_client(&mut self, id: &str) {
        self.clients.remove(id);
    }
}

impl eframe::App for ServerState {
    fn update(&mut self, ctx: &eframe::egui::Context, _frame: &mut eframe::Frame) {
        CentralPanel::default().show(ctx, |ui| {
            ui.label(format!("Num Clients: {}", self.clients.len()));
        });
    }
}
