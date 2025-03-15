pub mod models;
mod routes;
use crate::{
    error::ControlError,
    event::{ControlToUiEvent, ControlToVoiceEvent, UiToControlEvent},
    state::{AdminState, ClientState, OptionsState},
};
use axum::Router;
use log::{error, info};
use socketioxide::SocketIo;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::Mutex;
use tokio::sync::{broadcast, mpsc, RwLock};

pub async fn start_control(
    client_state: Arc<RwLock<ClientState>>,
    option_state: Arc<RwLock<OptionsState>>,
    admin_state: Arc<RwLock<AdminState>>,
    ui_rx: Arc<Mutex<mpsc::Receiver<UiToControlEvent>>>,
    voice_tx: broadcast::Sender<ControlToVoiceEvent>,
    ui_tx: broadcast::Sender<ControlToUiEvent>,
) -> Result<(), ControlError> {
    let addr = get_server_addr(&option_state).await?;

    let (layer, io) = SocketIo::builder()
        .with_state(client_state)
        .with_state(option_state)
        .with_state(admin_state)
        .with_state(ui_tx)
        .with_state(voice_tx)
        .build_layer();

    let app = Router::new()
        .nest("/api/v1", routes::get_router())
        .layer(layer);

    let listener = tokio::net::TcpListener::bind(addr)
        .await
        .map_err(|_| ControlError::InitError("Failed to open API listener.".to_string()))?;

    spawn_ui_event_handler(ui_rx).await?;

    info!(
        "Control server started on {}",
        listener.local_addr().unwrap()
    );

    axum::serve(listener, app).await.unwrap();
    Ok(())
}

async fn spawn_ui_event_handler(
    ui_rx: Arc<Mutex<mpsc::Receiver<UiToControlEvent>>>,
) -> Result<(), ControlError> {
    tokio::spawn(async move {
        let mut backoff_duration = Duration::from_millis(100);
        const MAX_BACKOFF: Duration = Duration::from_secs(5);

        while let Some(event) = ui_rx.lock().await.recv().await {
            match handle_ui_event(event).await {
                Ok(_) => {
                    backoff_duration = Duration::from_millis(100); // Reset backoff on success
                }
                Err(e) => {
                    error!("Control event handling error: {}", e);
                    tokio::time::sleep(backoff_duration).await;
                    backoff_duration = std::cmp::min(backoff_duration * 2, MAX_BACKOFF);
                }
            }
        }
    });
    Ok(())
}

async fn handle_ui_event(event: UiToControlEvent) -> Result<(), ControlError> {
    match event {
        UiToControlEvent::BanClient { client_id } => {
            info!("Banning client {}", client_id);
        }
        UiToControlEvent::UnbanClient { client_id } => {
            info!("Unbanning client {}", client_id);
        }
        UiToControlEvent::MuteClient { client_id } => {
            info!("Muting client {}", client_id);
        }
        UiToControlEvent::UnmuteClient { client_id } => {
            info!("Unmuting client {}", client_id);
        }
        UiToControlEvent::KickClient { client_id } => {
            info!("Kicking client {}", client_id);
        }
    }
    Ok(())
}

async fn get_server_addr(
    options_state: &Arc<RwLock<OptionsState>>,
) -> Result<String, ControlError> {
    let options_state = options_state.read().await;
    let ip = options_state.options.server.server_ip.clone();
    let port = options_state.options.server.server_port;

    if ip.is_empty() {
        return Err(ControlError::InitError(
            "Server IP cannot be empty".to_string(),
        ));
    }
    if port == 0 {
        return Err(ControlError::InitError("Invalid server port".to_string()));
    }

    Ok(format!("{}:{}", ip, port))
}
