use eframe::App;
use log::error;
use log::info;
use std::path::Path;
use std::sync::Arc;
use tokio::sync::RwLock;
use vngd_srs_server::state::{AdminState, ClientState, OptionsState};
use vngd_srs_server::{
    config::ServerOptions, error::ServerError, event::EventBus, gui::app::ServerGui,
    network::VoiceServer,
};

#[tokio::main]
async fn main() -> Result<(), ServerError> {
    // Initialize logging with default config if not found
    init_logging()
        .map_err(|e| ServerError::ConfigError(format!("Failed to initialize logging: {}", e)))?;

    // Load server configuration with default if not found
    let config = init_server_config()
        .map_err(|e| ServerError::ConfigError(format!("Failed to load configuration: {}", e)))?;

    let client_state = Arc::new(RwLock::new(ClientState::new()));
    let option_state = Arc::new(RwLock::new(OptionsState::new(config)));
    let admin_state = Arc::new(RwLock::new(AdminState::new()));

    let event_bus = EventBus::new();

    let mut server = VoiceServer::new(
        client_state.clone(),
        option_state.clone(),
        admin_state.clone(),
        event_bus.server_tx.clone(),
        event_bus.ui_rx,
    );

    // Spawn server task
    let server_handle = tokio::spawn(async move {
        if let Err(e) = server.start().await {
            error!("Server error: {}", e);
        }
    });

    let gui = ServerGui::new(
        client_state,
        option_state,
        admin_state,
        event_bus.ui_tx,
        event_bus.server_rx.resubscribe(),
    );

    let native_options = eframe::NativeOptions::default();

    // Run the GUI with proper error handling
    eframe::run_native(
        "VCS SRS Server",
        native_options,
        Box::new(|cc| Ok(Box::new(gui) as Box<dyn App>)),
    )
    .map_err(|e| ServerError::InternalError(format!("GUI error: {}", e)))?;

    // Cleanup
    server_handle.abort();
    Ok(())
}

fn init_logging() -> Result<(), ServerError> {
    const LOG_CONFIG_PATH: &str = "config/log4rs.yaml";

    if !Path::new(LOG_CONFIG_PATH).exists() {
        // Create config directory if it doesn't exist
        std::fs::create_dir_all("config").map_err(|e| {
            ServerError::ConfigError(format!("Failed to create config directory: {}", e))
        })?;

        // Default logging configuration
        let default_config = r#"
appenders:
    stdout:
        kind: console
        encoder:
            pattern: "{d(%Y-%m-%d %H:%M:%S)} {h({l})} {m}{n}"
        filters:
            - kind: threshold
              level: trace
    file:
        kind: file
        path: "log/vngd-srs-server.log"
        encoder:
            pattern: "[{d(%Y-%m-%d %H:%M:%S)} - {M}] {h({l})} | {m}{n}"
        filters:
            - kind: threshold
              level: warn

loggers:
  vngd_srs_server:
      level: trace
      appenders:
          - stdout
          - file
"#;

        // Create log directory if it doesn't exist
        std::fs::create_dir_all("log").map_err(|e| {
            ServerError::ConfigError(format!("Failed to create log directory: {}", e))
        })?;

        // Write default config
        std::fs::write(LOG_CONFIG_PATH, default_config).map_err(|e| {
            ServerError::ConfigError(format!("Failed to write default log config: {}", e))
        })?;

        info!(
            "Created default logging configuration at {}",
            LOG_CONFIG_PATH
        );
    }

    // Initialize logging
    log4rs::init_file(LOG_CONFIG_PATH, Default::default())
        .map_err(|e| ServerError::ConfigError(format!("Failed to initialize logging: {}", e)))?;

    Ok(())
}

fn init_server_config() -> Result<ServerOptions, ServerError> {
    const SERVER_CONFIG_PATH: &str = "config/server.toml";

    if !Path::new(SERVER_CONFIG_PATH).exists() {
        // Create config directory if it doesn't exist
        std::fs::create_dir_all("config").map_err(|e| {
            ServerError::ConfigError(format!("Failed to create config directory: {}", e))
        })?;

        // Create default configuration
        let default_config = ServerOptions::default();

        // Convert to TOML string
        let config_str = toml::to_string_pretty(&default_config).map_err(|e| {
            ServerError::ConfigError(format!("Failed to serialize default config: {}", e))
        })?;

        // Write default config
        std::fs::write(SERVER_CONFIG_PATH, config_str).map_err(|e| {
            ServerError::ConfigError(format!("Failed to write default server config: {}", e))
        })?;

        info!(
            "Created default server configuration at {}",
            SERVER_CONFIG_PATH
        );

        Ok(default_config)
    } else {
        // Load existing configuration
        let config_str = std::fs::read_to_string(SERVER_CONFIG_PATH).map_err(|e| {
            ServerError::ConfigError(format!("Failed to read server config: {}", e))
        })?;

        toml::from_str(&config_str)
            .map_err(|e| ServerError::ConfigError(format!("Failed to parse server config: {}", e)))
    }
}
