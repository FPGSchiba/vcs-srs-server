use std::sync::Arc;

use crate::{error::ServerError, state::SharedState};
use log::{error, info};
use tokio::{
    net::UdpSocket,
    sync::{broadcast, RwLock},
};

pub struct UdpHandler {
    socket: UdpSocket,
    state: Arc<RwLock<SharedState>>,
}

impl UdpHandler {
    pub fn new(socket: UdpSocket, state: Arc<RwLock<SharedState>>) -> Self {
        Self { socket, state }
    }

    pub async fn run(&mut self) -> Result<(), ServerError> {
        info!("UDP handler started on {}", self.socket.local_addr()?);
        loop {
            let mut buf = vec![0; 1024];
            tokio::select! {
                // Handle UDP packets
                result = self.socket.recv_from(&mut buf) => {
                    match result {
                        Ok((len, addr)) => {
                            // Handle received data
                            info!("Received {} bytes from {}", len, addr);
                        }
                        Err(e) => {
                            error!("UDP receive error: {}", e);
                        }
                    }
                }
            }
        }
        Ok(())
    }
}
