pub mod tcp_sync;
pub mod upd_voice;
pub mod utils;

use std::{
    collections::HashMap,
    sync::{
        mpsc::{channel, Receiver, Sender},
        Arc, Mutex, RwLock,
    },
};

use crate::states::{
    events::{ServerUIEvent, TCPServerEvent, UIServerEvent},
    server::{ServerOptions, ServerState},
};

pub struct SrsServer {
    pub tcp_server: Arc<Mutex<tcp_sync::SrsTcpServer>>,
    pub udp_server: Arc<Mutex<upd_voice::SrsVoiceServer>>,
    pub state: Arc<RwLock<ServerState>>,
    pub server_ui_sender: Sender<UIServerEvent>,
    pub server_ui_receiver: Receiver<ServerUIEvent>,
    server_tcp_receiver: Receiver<TCPServerEvent>,
}

impl SrsServer {
    pub fn new(
        srs_server: tcp_sync::SrsTcpServer,
        voice_server: upd_voice::SrsVoiceServer,
        config: ServerOptions,
        server_ui_sender: Sender<UIServerEvent>,
        server_ui_receiver: Receiver<ServerUIEvent>,
        server_tcp_receiver: Receiver<TCPServerEvent>,
    ) -> std::io::Result<Self> {
        Ok(Self {
            tcp_server: Arc::new(Mutex::new(srs_server)),
            udp_server: Arc::new(Mutex::new(voice_server)),
            server_ui_sender,
            server_ui_receiver,
            state: Arc::new(RwLock::new(ServerState {
                clients: HashMap::new(),
                options: config,
                version: crate::VERSION.to_owned(),
            })),
            server_tcp_receiver,
        })
    }

    pub fn start(&mut self) {
        let tcp_server = Arc::clone(&self.tcp_server);
        let udp_server = Arc::clone(&self.udp_server);
        let state = Arc::clone(&self.state);

        std::thread::Builder::new()
            .name("TCP-Handler".to_string())
            .spawn(move || {
                let mut tcp_server = tcp_server.lock().unwrap();
                tcp_server.start(state);
            })
            .unwrap();

        let state = Arc::clone(&self.state);
        std::thread::Builder::new()
            .name("UDP-Handler".to_string())
            .spawn(move || {
                let mut udp_server = udp_server.lock().unwrap();
                udp_server.start(state).unwrap();
            })
            .unwrap();

        loop {
            // Server UI Events
            match self.server_ui_receiver.try_recv() {
                Ok(event) => match event {
                    ServerUIEvent::BanClient(id) => {
                        log::info!("Banning client: {}", id);
                    }
                    ServerUIEvent::UnbanClient(id) => {
                        log::info!("Unbanning client: {}", id);
                    }
                    ServerUIEvent::Stop => {
                        log::info!("Shutting down server");
                    }
                    ServerUIEvent::Start => {
                        log::info!("Starting server");
                    }
                    ServerUIEvent::KickClient(id) => {
                        log::info!("Kicking client: {}", id);
                    }
                    ServerUIEvent::MuteClient(id) => {
                        log::info!("Muting client: {}", id);
                    }
                    ServerUIEvent::UnmuteClient(id) => {
                        log::info!("Unmuting client: {}", id);
                    }
                },
                Err(e) => {
                    log::error!("Error receiving event: {}", e);
                }
            }

            // TCP Server Events
            match self.server_tcp_receiver.try_recv() {
                Ok(event) => match event {
                    TCPServerEvent::BanClient(id) => {
                        log::info!("Banning client: {}", id);
                    }
                    TCPServerEvent::UnbanClient(id) => {
                        log::info!("Unbanning client: {}", id);
                    }
                    TCPServerEvent::ClientConnected(id) => {
                        log::info!("Client connected: {}", id);
                    }
                    TCPServerEvent::ClientDisconnected(id) => {
                        log::info!("Client disconnected: {}", id);
                    }
                    TCPServerEvent::KickClient(id) => {
                        log::info!("Kicking client: {}", id);
                    }
                    TCPServerEvent::MuteClient(id) => {
                        log::info!("Muting client: {}", id);
                    }
                    TCPServerEvent::UnmuteClient(id) => {
                        log::info!("Unmuting client: {}", id);
                    }
                },
                Err(e) => {
                    log::error!("Error receiving event: {}", e);
                }
            }
        }
    }
}
