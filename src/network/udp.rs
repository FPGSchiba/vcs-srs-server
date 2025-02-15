use std::sync::Arc;

use crate::{error::ServerError};
use log::{error, info};
use tokio::{
    net::UdpSocket,
    sync::{broadcast, RwLock},
};
use crate::state::{AdminState, ClientState, OptionsState};

pub struct UdpHandler {
    socket: UdpSocket,
    client_sate: Arc<RwLock<ClientState>>,
    option_state: Arc<RwLock<OptionsState>>,
    admin_state: Arc<RwLock<AdminState>>,
}

impl UdpHandler {
    pub fn new(socket: UdpSocket, client_sate: Arc<RwLock<ClientState>>, option_state: Arc<RwLock<OptionsState>>, admin_state: Arc<RwLock<AdminState>>) -> Self {
        Self { socket, client_sate, option_state, admin_state }
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
