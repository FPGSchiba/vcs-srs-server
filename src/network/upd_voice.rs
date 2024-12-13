use std::{
    net::SocketAddr,
    sync::{Arc, Mutex},
};

use crate::{states::server::ServerState, VERSION};

pub struct SrsVoiceServer {
    socket: std::net::UdpSocket,
    connections: Vec<SocketAddr>,
    state: Arc<Mutex<ServerState>>,
}

impl SrsVoiceServer {
    pub fn new(address: &String, port: &u16) -> std::io::Result<Self> {
        let socket = std::net::UdpSocket::bind(format!("{}:{}", address, port))?;
        Ok(Self {
            socket,
            connections: Vec::new(),
            state: Arc::new(Mutex::new(ServerState {
                clients: std::collections::HashMap::new(),
                options: Default::default(),
                version: VERSION.to_owned(),
            })),
        })
    }

    pub fn start(&mut self, state: Arc<Mutex<ServerState>>) -> std::io::Result<()> {
        self.state = state;
        let mut buf = [0; 512];
        loop {
            let (amt, src) = self.socket.recv_from(&mut buf)?;
            println!("Received {} bytes from {}", amt, src);
            println!("{}", String::from_utf8_lossy(&buf));
        }
    }
}
