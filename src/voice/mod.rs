mod handler;

use crate::state::{AdminState, ClientState, OptionsState};
use handler::UdpHandler;
use log::{error, info};
use std::sync::Arc;
use std::time::Duration;
use tokio::net::UdpSocket;
use tokio::sync::{broadcast, mpsc, Mutex, RwLock};

use crate::error::VoiceServerError;
use crate::event::{ControlToVoiceEvent, UiToVoiceEvent, VoiceToUiEvent};

pub struct VoiceServer {
    pub client_state: Arc<RwLock<ClientState>>,
    pub options_state: Arc<RwLock<OptionsState>>,
    pub admin_state: Arc<RwLock<AdminState>>,
    udp_handler: Option<Arc<RwLock<UdpHandler>>>,
    event_tx: broadcast::Sender<VoiceToUiEvent>,
    ui_rx: Arc<Mutex<mpsc::Receiver<UiToVoiceEvent>>>,
    control_rx: Arc<Mutex<broadcast::Receiver<ControlToVoiceEvent>>>,
}

impl VoiceServer {
    pub fn new(
        client_state: Arc<RwLock<ClientState>>,
        options_state: Arc<RwLock<OptionsState>>,
        admin_state: Arc<RwLock<AdminState>>,
        event_tx: broadcast::Sender<VoiceToUiEvent>,
        ui_rx: mpsc::Receiver<UiToVoiceEvent>,
        control_rx: broadcast::Receiver<ControlToVoiceEvent>,
    ) -> VoiceServer {
        let server = Self {
            client_state,
            options_state,
            admin_state,
            udp_handler: None,
            event_tx,
            ui_rx: Arc::new(Mutex::new(ui_rx)),
            control_rx: Arc::new(Mutex::new(control_rx)),
        };

        server
    }

    async fn send_event(&self, event: VoiceToUiEvent) -> Result<(), VoiceServerError> {
        self.event_tx.send(event).map_err(|e| {
            VoiceServerError::EventError(format!("Failed to send event to UI: {}", e))
        })?;
        Ok(())
    }

    pub async fn start(&mut self) -> Result<(), VoiceServerError> {
        // Get server configuration with proper error handling
        let server_config = self.get_server_config().await?;

        // Initialize handlers with proper error handling
        self.initialize_handlers(&server_config).await?;

        // Spawn event handlers with proper error handling
        self.spawn_handlers().await?;

        info!("Voice server started successfully");
        Ok(())
    }

    async fn get_server_config(&self) -> Result<String, VoiceServerError> {
        let options_state = self.options_state.read().await;
        let ip = options_state.options.server.server_ip.clone();
        let port = options_state.options.server.server_port;

        if ip.is_empty() {
            return Err(VoiceServerError::InitError(
                "Server IP cannot be empty".to_string(),
            ));
        }
        if port == 0 {
            return Err(VoiceServerError::InitError(
                "Invalid server port".to_string(),
            ));
        }

        Ok(format!("{}:{}", ip, port))
    }

    async fn initialize_handlers(&mut self, address: &str) -> Result<(), VoiceServerError> {
        // Initialize UDP handler with timeout
        let udp_socket = tokio::time::timeout(Duration::from_secs(5), UdpSocket::bind(address))
            .await
            .map_err(|_| VoiceServerError::InitError("UDP socket binding timeout".to_string()))??;

        let udp = Arc::new(RwLock::new(UdpHandler::new(
            udp_socket,
            Arc::clone(&self.client_state),
            Arc::clone(&self.options_state),
            Arc::clone(&self.admin_state),
        )));

        self.udp_handler = Some(udp);

        Ok(())
    }

    async fn spawn_handlers(&mut self) -> Result<(), VoiceServerError> {
        self.spawn_ui_event_handler().await?;
        self.spawn_udp_handler().await?;
        Ok(())
    }

    async fn spawn_ui_event_handler(&self) -> Result<(), VoiceServerError> {
        let event_tx = self.event_tx.clone();
        let ui_rx = self.ui_rx.clone();

        tokio::spawn(async move {
            let mut backoff_duration = Duration::from_millis(100);
            const MAX_BACKOFF: Duration = Duration::from_secs(5);

            while let Some(event) = ui_rx.lock().await.recv().await {
                match handle_ui_event(event, &event_tx).await {
                    Ok(_) => {
                        backoff_duration = Duration::from_millis(100); // Reset backoff on success
                    }
                    Err(e) => {
                        error!("UI event handling error: {}", e);
                        tokio::time::sleep(backoff_duration).await;
                        backoff_duration = std::cmp::min(backoff_duration * 2, MAX_BACKOFF);
                    }
                }
            }
        });

        Ok(())
    }

    async fn spawn_control_event_handler(&self) -> Result<(), VoiceServerError> {
        let control_rx = self.control_rx.clone();

        tokio::spawn(async move {
            let mut backoff_duration = Duration::from_millis(100);
            const MAX_BACKOFF: Duration = Duration::from_secs(5);

            while let Ok(event) = control_rx.lock().await.recv().await {
                match handle_control_event(event).await {
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

    async fn spawn_udp_handler(&self) -> Result<(), VoiceServerError> {
        let udp = self.udp_handler.as_ref().ok_or_else(|| {
            VoiceServerError::HandlerError("UDP handler not initialized".to_string())
        })?;

        let udp_clone = Arc::clone(udp);
        tokio::spawn(async move {
            let mut retry_count = 0;
            const MAX_RETRIES: u32 = 3;

            while retry_count < MAX_RETRIES {
                match udp_clone.write().await.run().await {
                    Ok(_) => break,
                    Err(e) => {
                        error!(
                            "UDP handler error (attempt {}/{}): {}",
                            retry_count + 1,
                            MAX_RETRIES,
                            e
                        );
                        retry_count += 1;
                        tokio::time::sleep(Duration::from_secs(1)).await;
                    }
                }
            }

            if retry_count == MAX_RETRIES {
                error!("UDP handler failed after {} retries", MAX_RETRIES);
            }
        });

        Ok(())
    }
}

async fn handle_ui_event(
    event: UiToVoiceEvent,
    event_tx: &broadcast::Sender<VoiceToUiEvent>,
) -> Result<(), VoiceServerError> {
    match event {}
}

async fn handle_control_event(event: ControlToVoiceEvent) -> Result<(), VoiceServerError> {
    match event {
        ControlToVoiceEvent::ClientConnected { client_id } => {
            info!("[VOICE] Received new Client: {}", client_id);
            Ok(())
        }
        ControlToVoiceEvent::ClientDisconnected { client_id } => {
            info!("[VOICE] Client disconnected: {}", client_id);
            Ok(())
        }
        ControlToVoiceEvent::MuteClient { client_id } => {
            info!("[VOICE] Mute client: {}", client_id);
            Ok(())
        }
        ControlToVoiceEvent::UnmuteClient { client_id } => {
            info!("[VOICE] Unmute client: {}", client_id);
            Ok(())
        }
    }
}
