pub mod client;

use std::collections::HashMap;
use std::net::SocketAddr;
use thiserror::Error;

use crate::config::ServerOptions;
use crate::state::client::RadioInformation;
use client::Client;

pub struct ClientState {
    pub clients: HashMap<SocketAddr, Client>,
}

pub struct OptionsState {
    pub options: ServerOptions,
}

pub struct AdminState {
    pub running: bool,
}

#[derive(Debug, Error)]
pub enum ClientStateError {
    #[error("Client not found: {0}")]
    ClientNotFound(SocketAddr),
    #[error("Invalid client data: {0}")]
    InvalidClientData(String),
}

impl ClientState {
    pub fn new() -> Self {
        Self {
            clients: HashMap::new(),
        }
    }

    pub fn add_client(&mut self, addr: SocketAddr, client: Client) -> Result<(), ClientStateError> {
        if self.clients.contains_key(&addr) {
            return Err(ClientStateError::InvalidClientData(format!(
                "Client already exists: {}",
                addr
            )));
        }
        self.clients.insert(addr, client);
        Ok(())
    }

    pub fn remove_client(&mut self, addr: &SocketAddr) -> Result<(), ClientStateError> {
        self.clients
            .remove(addr)
            .ok_or_else(|| ClientStateError::ClientNotFound(*addr))?;
        Ok(())
    }

    pub fn update_radio_information(
        &mut self,
        addr: &SocketAddr,
        radio_info: RadioInformation,
    ) -> Result<(), ClientStateError> {
        let client = self
            .clients
            .get_mut(addr)
            .ok_or_else(|| ClientStateError::ClientNotFound(*addr))?;

        client.radio_information = Some(radio_info);
        Ok(())
    }

    pub fn get_client(&self, addr: &SocketAddr) -> Result<&Client, ClientStateError> {
        self.clients
            .get(addr)
            .ok_or_else(|| ClientStateError::ClientNotFound(*addr))
    }
}

impl OptionsState {
    pub fn new(options: ServerOptions) -> Self {
        Self { options }
    }
}

impl AdminState {
    pub fn new() -> Self {
        Self { running: false }
    }
}
