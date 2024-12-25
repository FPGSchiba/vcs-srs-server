use log::{error, info};
use std::sync::Arc;
use tokio::net::{TcpListener, UdpSocket};
use tokio::sync::{broadcast, mpsc, Mutex, RwLock};

use tcp::handler::TcpHandler;
use udp::UdpHandler;

use crate::event::{ServerToUiEvent, UiToServerEvent};
use crate::{error::ServerError, network::types::ConnectionEvent, state::SharedState};

pub mod tcp;
pub mod types;
pub mod udp;

pub struct VoiceServer {
    pub state: Arc<RwLock<SharedState>>,
    udp_handler: Option<Arc<RwLock<UdpHandler>>>,
    tcp_handler: Option<Arc<RwLock<TcpHandler>>>,
    event_tx: broadcast::Sender<ServerToUiEvent>,
    ui_rx: Arc<Mutex<mpsc::Receiver<UiToServerEvent>>>,
    connection_tx: mpsc::Sender<ConnectionEvent>,
    connection_rx: Arc<Mutex<mpsc::Receiver<ConnectionEvent>>>,
}

impl VoiceServer {
    pub fn new(
        state: Arc<RwLock<SharedState>>,
        event_tx: broadcast::Sender<ServerToUiEvent>,
        ui_rx: mpsc::Receiver<UiToServerEvent>,
    ) -> Self {
        let (connection_tx, connection_rx) = mpsc::channel::<ConnectionEvent>(32);

        let mut server = Self {
            state: state,
            udp_handler: None,
            tcp_handler: None,
            event_tx,
            ui_rx: Arc::new(Mutex::new(ui_rx)),
            connection_tx,
            connection_rx: Arc::new(Mutex::new(connection_rx)),
        };
        server.spawn_ui_event_handler();
        server
    }

    async fn send_event(&self, event: ServerToUiEvent) {
        if let Err(e) = self.event_tx.send(event) {
            eprintln!("Failed to send event to UI: {}", e);
        }
    }

    pub async fn start(&mut self) -> Result<(), ServerError> {
        // Create shutdown channels
        let ip = self.state.read().await.options.server.server_ip.clone();
        let port = self.state.read().await.options.server.server_port;
        let address = format!("{}:{}", ip, port);

        // Initialize UDP handler
        let udp_socket = UdpSocket::bind(&address).await?;
        let udp = Arc::new(RwLock::new(UdpHandler::new(
            udp_socket,
            Arc::clone(&self.state),
        )));

        // Initialize TCP handler
        let tcp_listener = TcpListener::bind(address).await?;
        let tcp = Arc::new(RwLock::new(TcpHandler::new(
            tcp_listener,
            Arc::clone(&self.state),
            self.connection_tx.clone(),
        )));

        let task_tcp = Arc::clone(&tcp);
        // Spawn handlers
        tokio::spawn(async move {
            if let Err(e) = task_tcp.write().await.run().await {
                error!("TCP handler error: {}", e);
            }
        });
        let task_udp = Arc::clone(&udp);
        tokio::spawn(async move {
            if let Err(e) = task_udp.write().await.run().await {
                error!("UDP handler error: {}", e);
            }
        });

        // Store handlers
        self.udp_handler = Some(udp);
        self.tcp_handler = Some(tcp);

        // Spawn event handler
        self.spawn_connection_event_handler();

        info!("Voice server started");
        Ok(())
    }

    async fn spawn_ui_event_handler(&mut self) {
        let state = Arc::clone(&self.state);
        let event_tx = self.event_tx.clone();
        let ui_rx = self.ui_rx.clone();

        tokio::spawn(async move {
            while let Some(event) = ui_rx.lock().await.recv().await {
                match event {
                    _ => {
                        error!("UI event not implemented");
                    }
                }
            }
        });
    }

    fn spawn_connection_event_handler(&self) {
        let state = Arc::clone(&self.state);
        let event_rx = Arc::clone(&self.connection_rx);

        tokio::spawn(async move {
            while let Some(event) = event_rx.lock().await.recv().await {
                match event {
                    ConnectionEvent::LoginSuccess(client) => {
                        info!("Client connected: {:?}", client);
                        // Handle connection in state
                    }
                    ConnectionEvent::ClientDisconnect(id) => {
                        info!("Client disconnected: {}", id);
                        // Handle disconnection in state
                    }
                    ConnectionEvent::ServerSettings => {
                        error!("Client {}", "Server settings not implemented");
                        // Handle error in state
                    } // Handle other events...
                }
            }
        });
    }
}
