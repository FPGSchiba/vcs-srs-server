use log::{error, info};
use std::sync::Arc;
use tokio::{
    net::TcpListener,
    sync::{broadcast, mpsc, RwLock},
};

use crate::{error::ServerError, network::types::ConnectionEvent, state::SharedState};

pub struct TcpHandler {
    listener: TcpListener,
    state: Arc<RwLock<SharedState>>,
    event_tx: mpsc::Sender<ConnectionEvent>,
}

impl TcpHandler {
    pub fn new(
        listener: TcpListener,
        state: Arc<RwLock<SharedState>>,
        event_tx: mpsc::Sender<ConnectionEvent>,
    ) -> Self {
        Self {
            listener,
            state,
            event_tx,
        }
    }

    pub async fn run(&mut self) -> Result<(), ServerError> {
        info!("TCP handler started on {}", self.listener.local_addr()?);

        loop {
            tokio::select! {
                // Accept new connections
                result = self.listener.accept() => {
                    match result {
                        Ok((stream, addr)) => {
                            info!("New TCP connection from {}", addr);
                            let event_tx = self.event_tx.clone();
                            self.spawn_client_handler(stream, addr, event_tx);
                        }
                        Err(e) => {
                            error!("TCP accept error: {}", e);
                            // Consider if this error should break the loop
                            if e.kind() == std::io::ErrorKind::Other {
                                break;
                            }
                        }
                    }
                }
            }
        }

        info!("TCP handler stopped");
        Ok(())
    }

    fn spawn_client_handler(
        &self,
        stream: tokio::net::TcpStream,
        addr: std::net::SocketAddr,
        event_tx: mpsc::Sender<ConnectionEvent>,
    ) {
        let state = Arc::clone(&self.state);

        tokio::spawn(async move {
            let client = crate::network::tcp::connection::ClientConnection::new(
                stream, addr, state, event_tx,
            );

            if let Err(e) = client.handle_connection().await {
                error!("Client handler error for {}: {}", addr, e);
            }
        });
    }
}
