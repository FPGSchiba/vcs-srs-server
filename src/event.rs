use tokio::sync::{broadcast, mpsc};

// Events from UI to Server
#[derive(Debug, Clone)]
pub enum UiToServerEvent {
    // Add more events as needed
}

// Events from Server to UI
#[derive(Debug, Clone)]
pub enum ServerToUiEvent {
    ClientConnected { client_id: String },
    ClientDisconnected { client_id: String },
    Error { message: String },
    // Add more events as needed
}

pub struct EventBus {
    pub ui_tx: mpsc::Sender<UiToServerEvent>,
    pub ui_rx: mpsc::Receiver<UiToServerEvent>,
    pub server_tx: broadcast::Sender<ServerToUiEvent>,
    pub server_rx: broadcast::Receiver<ServerToUiEvent>,
}

impl EventBus {
    pub fn new() -> Self {
        let (ui_tx, ui_rx) = mpsc::channel(100);
        let (server_tx, server_rx) = broadcast::channel(100);
        Self {
            ui_tx,
            ui_rx,
            server_tx,
            server_rx,
        }
    }
}
