pub mod client;

use std::collections::HashMap;
use std::net::SocketAddr;

use crate::config::ServerOptions;
use client::Client;
use crate::state::client::RadioInformation;

pub struct ClientState {
    pub clients: HashMap<SocketAddr, Client>,
}

pub struct OptionsState {
    pub options: ServerOptions,
}

pub struct AdminState {
    pub running: bool,
}

impl ClientState {
    pub fn new() -> Self {
        Self {
            clients: HashMap::new(),
        }
    }

    pub fn add_client(&mut self, addr: SocketAddr, client: Client) {
        self.clients.insert(addr, client);
    }

    pub fn remove_client(&mut self, addr: &SocketAddr) {
        self.clients.remove(addr);
    }

    pub fn update_radio_information(&mut self, addr: &SocketAddr, radio_info: RadioInformation) {
        if let Some(client) = self.clients.get_mut(addr) {
            client.radio_information = Some(radio_info);
        }
    }
}

impl OptionsState {
    pub fn new(options: ServerOptions) -> Self {
        Self {
            options,
        }
    }
}

impl AdminState {
    pub fn new() -> Self {
        Self {
            running: false,
        }
    }
}
