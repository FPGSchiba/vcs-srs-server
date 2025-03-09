use log::{error, info};
use std::sync::Arc;
use tokio::{
    net::TcpListener,
    sync::{mpsc, RwLock},
};

use crate::state::{AdminState, ClientState, OptionsState};
use crate::{error::ServerError, network::types::ConnectionEvent};

pub struct TcpHandler {
    listener: TcpListener,
    client_sate: Arc<RwLock<ClientState>>,
    options_state: Arc<RwLock<OptionsState>>,
    admin_state: Arc<RwLock<AdminState>>,
    event_tx: mpsc::Sender<ConnectionEvent>,
}

impl TcpHandler {
    pub fn new(
        listener: TcpListener,
        client_sate: Arc<RwLock<ClientState>>,
        options_state: Arc<RwLock<OptionsState>>,
        admin_state: Arc<RwLock<AdminState>>,
        event_tx: mpsc::Sender<ConnectionEvent>,
    ) -> Self {
        Self {
            listener,
            client_sate,
            options_state,
            admin_state,
            event_tx,
        }
    }

    pub async fn run(&mut self) -> Result<(), ServerError> {
        info!("TCP handler started on {}", self.listener.local_addr()?);

        loop {
            match self.listener.accept().await {
                Ok((stream, addr)) => {
                    info!("New TCP connection from {}", addr);
                    let event_tx = self.event_tx.clone();
                    if let Err(e) = self.spawn_client_handler(stream, addr, event_tx).await {
                        error!("Failed to spawn client handler: {}", e);
                        continue;
                    }
                }
                Err(e) => {
                    error!("TCP accept error: {}", e);
                    if e.kind() == std::io::ErrorKind::Other {
                        return Err(ServerError::NetworkError(e));
                    }
                }
            }
        }
    }

    async fn spawn_client_handler(
        &self,
        stream: tokio::net::TcpStream,
        addr: std::net::SocketAddr,
        event_tx: mpsc::Sender<ConnectionEvent>,
    ) -> Result<(), ServerError> {
        let client_state = Arc::clone(&self.client_sate);
        let options_state = Arc::clone(&self.options_state);
        let admin_state = Arc::clone(&self.admin_state);

        tokio::spawn(async move {
            let client = crate::network::tcp::connection::ClientConnection::new(
                stream,
                addr,
                client_state,
                options_state,
                admin_state,
                event_tx,
            );

            if let Err(e) = client.handle_connection().await {
                error!("Client handler error for {}: {}", addr, e);
            }
        });

        Ok(())
    }
}
