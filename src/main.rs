use eframe::App;
use log::error;
use std::sync::Arc;
use tokio::sync::RwLock;
use vngd_srs_server::state::{AdminState, ClientState, OptionsState};
use vngd_srs_server::{
    config::ServerOptions, error::ServerError, event::EventBus, gui::app::ServerGui,
    network::VoiceServer,
};

#[tokio::main]
async fn main() -> Result<(), ServerError> {
    // Initialize logging
    log4rs::init_file("config/log4rs.yaml", Default::default())
        .map_err(|e| ServerError::ConfigError(format!("Failed to initialize logging: {}", e)))?;

    // Load configuration
    let config = ServerOptions::from_config_file("config/server.toml")
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
