use std::sync::Arc;
use tokio::sync::RwLock;
use vngd_srs_server::{
    config::ServerOptions, error::ServerError, event::EventBus, gui::app::ServerGui,
    network::VoiceServer, state::SharedState,
};

#[tokio::main]
async fn main() -> Result<(), ServerError> {
    log4rs::init_file("config/log4rs.yaml", Default::default()).unwrap();
    let config = ServerOptions::from_config_file("config/server.toml").unwrap();
    let state = Arc::new(RwLock::new(SharedState::new(config)));

    let event_bus = EventBus::new();

    let mut server = VoiceServer::new(state.clone(), event_bus.server_tx.clone(), event_bus.ui_rx);

    // Spawn the server task to handle UI events
    let server_handle = tokio::spawn(async move {
        let _ = server.start().await;
    });

    // Create and run GUI
    let gui = ServerGui::new(
        state.clone(),
        event_bus.ui_tx.clone(),
        event_bus.server_rx.resubscribe(),
    );

    let native_options = eframe::NativeOptions::default();
    let _ = eframe::run_native(
        "VCS SRS Server",
        native_options,
        Box::new(|_cc| Ok(Box::new(gui) as Box<dyn eframe::App>)),
    );

    server_handle.abort();
    Ok(())
}
