use log::{error, info};
use std::sync::Arc;
use std::time::Duration;
use tokio::net::{TcpListener, UdpSocket};
use tokio::sync::{broadcast, mpsc, Mutex, RwLock};

use tcp::handler::TcpHandler;
use udp::UdpHandler;

use crate::error::VoiceServerError;
use crate::event::{ServerToUiEvent, UiToServerEvent};
use crate::state::{AdminState, ClientState, OptionsState};
use crate::{error::ServerError, network::types::ConnectionEvent};

pub mod tcp;
pub mod types;
pub mod udp;

pub struct VoiceServer {
    pub client_state: Arc<RwLock<ClientState>>,
    pub options_state: Arc<RwLock<OptionsState>>,
    pub admin_state: Arc<RwLock<AdminState>>,
    udp_handler: Option<Arc<RwLock<UdpHandler>>>,
    tcp_handler: Option<Arc<RwLock<TcpHandler>>>,
    event_tx: broadcast::Sender<ServerToUiEvent>,
    ui_rx: Arc<Mutex<mpsc::Receiver<UiToServerEvent>>>,
    connection_tx: mpsc::Sender<ConnectionEvent>,
    connection_rx: Arc<Mutex<mpsc::Receiver<ConnectionEvent>>>,
}

impl VoiceServer {
    pub fn new(
        client_state: Arc<RwLock<ClientState>>,
        options_state: Arc<RwLock<OptionsState>>,
        admin_state: Arc<RwLock<AdminState>>,
        event_tx: broadcast::Sender<ServerToUiEvent>,
        ui_rx: mpsc::Receiver<UiToServerEvent>,
    ) -> VoiceServer {
        let (connection_tx, connection_rx) = mpsc::channel::<ConnectionEvent>(32);

        let server = Self {
            client_state,
            options_state,
            admin_state,
            udp_handler: None,
            tcp_handler: None,
            event_tx,
            ui_rx: Arc::new(Mutex::new(ui_rx)),
            connection_tx,
            connection_rx: Arc::new(Mutex::new(connection_rx)),
        };

        server
    }

    async fn send_event(&self, event: ServerToUiEvent) -> Result<(), VoiceServerError> {
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

        // Initialize TCP handler with timeout
        let tcp_listener = tokio::time::timeout(Duration::from_secs(5), TcpListener::bind(address))
            .await
            .map_err(|_| {
                VoiceServerError::InitError("TCP listener binding timeout".to_string())
            })??;

        let tcp = Arc::new(RwLock::new(TcpHandler::new(
            tcp_listener,
            Arc::clone(&self.client_state),
            Arc::clone(&self.options_state),
            Arc::clone(&self.admin_state),
            self.connection_tx.clone(),
        )));

        self.udp_handler = Some(udp);
        self.tcp_handler = Some(tcp);

        Ok(())
    }

    async fn spawn_handlers(&mut self) -> Result<(), VoiceServerError> {
        self.spawn_ui_event_handler().await?;
        self.spawn_connection_event_handler()?;
        self.spawn_tcp_handler().await?;
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

    fn spawn_connection_event_handler(&self) -> Result<(), VoiceServerError> {
        let event_rx = Arc::clone(&self.connection_rx);
        let client_state = Arc::clone(&self.client_state);

        tokio::spawn(async move {
            while let Some(event) = event_rx.lock().await.recv().await {
                if let Err(e) = handle_connection_event(event, &client_state).await {
                    error!("Connection event handling error: {}", e);
                }
            }
        });

        Ok(())
    }

    async fn spawn_tcp_handler(&self) -> Result<(), VoiceServerError> {
        let tcp = self.tcp_handler.as_ref().ok_or_else(|| {
            VoiceServerError::HandlerError("TCP handler not initialized".to_string())
        })?;

        let tcp_clone = Arc::clone(tcp);
        tokio::spawn(async move {
            let mut retry_count = 0;
            const MAX_RETRIES: u32 = 3;

            while retry_count < MAX_RETRIES {
                match tcp_clone.write().await.run().await {
                    Ok(_) => break,
                    Err(e) => {
                        error!(
                            "TCP handler error (attempt {}/{}): {}",
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
                error!("TCP handler failed after {} retries", MAX_RETRIES);
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
    event: UiToServerEvent,
    event_tx: &broadcast::Sender<ServerToUiEvent>,
) -> Result<(), VoiceServerError> {
    match event {
        _ => {
            error!("UI event not implemented");
            // Return proper error instead of ignoring
            Err(VoiceServerError::EventError(
                "Unimplemented UI event".to_string(),
            ))
        }
    }
}

async fn handle_connection_event(
    event: ConnectionEvent,
    client_state: &Arc<RwLock<ClientState>>,
) -> Result<(), VoiceServerError> {
    match event {
        ConnectionEvent::LoginSuccess(client) => {
            info!("Client connected: {:?}", client);
            if let Some(addr) = client.addr {
                client_state.write().await.add_client(addr, client);
            }
            Ok(())
        }
        ConnectionEvent::ClientDisconnect(id) => {
            info!("Client disconnected: {}", id);
            // Implement proper client removal
            Ok(())
        }
        ConnectionEvent::ServerSettings => {
            error!("Server settings not implemented");
            Err(VoiceServerError::EventError(
                "Server settings not implemented".to_string(),
            ))
        }
    }
}
