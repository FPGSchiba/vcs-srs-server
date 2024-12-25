pub mod client;

use std::collections::HashMap;
use std::net::SocketAddr;

use crate::config::ServerOptions;
use client::Client;

pub struct SharedState {
    pub clients: HashMap<SocketAddr, Client>,
    pub options: ServerOptions,
}

impl SharedState {
    pub fn new(options: ServerOptions) -> Self {
        Self {
            clients: HashMap::new(),
            options,
        }
    }
}
