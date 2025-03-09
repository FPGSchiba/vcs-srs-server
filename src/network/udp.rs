use std::net::SocketAddr;
use std::sync::Arc;

use crate::error::ServerError;
use crate::state::{AdminState, ClientState, OptionsState};
use log::{error, info};
use tokio::{
    net::UdpSocket,
    sync::{broadcast, RwLock},
};

pub struct UdpHandler {
    socket: UdpSocket,
    client_sate: Arc<RwLock<ClientState>>,
    option_state: Arc<RwLock<OptionsState>>,
    admin_state: Arc<RwLock<AdminState>>,
}

impl UdpHandler {
    pub fn new(
        socket: UdpSocket,
        client_sate: Arc<RwLock<ClientState>>,
        option_state: Arc<RwLock<OptionsState>>,
        admin_state: Arc<RwLock<AdminState>>,
    ) -> Self {
        Self {
            socket,
            client_sate,
            option_state,
            admin_state,
        }
    }

    pub async fn run(&mut self) -> Result<(), ServerError> {
        info!("UDP handler started on {}", self.socket.local_addr()?);

        let mut buf = vec![0; 1024];
        loop {
            match self.socket.recv_from(&mut buf).await {
                Ok((len, addr)) => {
                    if let Err(e) = self.handle_packet(&buf[..len], addr).await {
                        error!("Error handling UDP packet from {}: {}", addr, e);
                        continue;
                    }
                }
                Err(e) => {
                    // Only return fatal errors, continue on temporary ones
                    match e.kind() {
                        std::io::ErrorKind::ConnectionReset
                        | std::io::ErrorKind::ConnectionAborted => {
                            continue;
                        }
                        _ => return Err(ServerError::NetworkError(e)),
                    }
                }
            }
        }
    }

    async fn handle_packet(&mut self, data: &[u8], addr: SocketAddr) -> Result<(), ServerError> {
        // Add proper packet handling with error propagation
        match String::from_utf8(data.to_vec()) {
            Ok(message) => {
                self.process_message(&message, addr).await?;
            }
            Err(e) => {
                return Err(ServerError::ProtocolError(format!(
                    "Invalid UTF-8 in UDP packet: {}",
                    e
                )));
            }
        }
        Ok(())
    }

    async fn process_message(
        &mut self,
        message: &str,
        addr: SocketAddr,
    ) -> Result<(), ServerError> {
        // Add message processing logic with proper error handling
        Ok(())
    }
}
