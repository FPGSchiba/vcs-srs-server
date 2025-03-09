use serde::{Deserialize, Serialize};

use crate::error::ConfigError;

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
    #[serde(rename = "BAN_LIST_FILE_PATH")]
    pub ban_list_file_path: String,
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
            ban_list_file_path: "banlist.json".to_owned(),
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
    pub fn validate(&self) -> Result<(), ConfigError> {
        // Validate server port
        if self.server.server_port == 0 {
            return Err(ConfigError::ValidationError(
                "Server port cannot be 0".to_string(),
            ));
        }

        // Validate frequencies
        if self.general.test_frequencies.is_empty() {
            return Err(ConfigError::ValidationError(
                "Test frequencies cannot be empty".to_string(),
            ));
        }

        // Validate global lobby frequency
        if self.general.global_lobby_freq <= 0.0 {
            return Err(ConfigError::ValidationError(
                "Global lobby frequency must be positive".to_string(),
            ));
        }

        Ok(())
    }

    pub fn to_config_file(&self, filename: &str) -> Result<(), ConfigError> {
        self.validate()?;
        let config_str = toml::to_string(self)?; // This will now convert toml::ser::Error to ConfigError::TomlSerError
        std::fs::write(filename, config_str)?;
        Ok(())
    }

    pub fn from_config_file(filename: &str) -> Result<Self, ConfigError> {
        if !std::path::Path::new(filename).exists() {
            Self::default().to_config_file(filename)?;
        }
        let config_str = std::fs::read_to_string(filename)?;
        let config = toml::from_str(&config_str)?; // This will convert toml::de::Error to ConfigError::TomlDeError
        Ok(config)
    }
}
