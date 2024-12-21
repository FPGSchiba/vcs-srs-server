pub mod tcp_sync;
pub mod upd_voice;
pub mod utils;

use std::{
    collections::HashMap,
    sync::{
        mpsc::{Receiver, Sender},
        Arc, Mutex,
    },
};

use crate::states::{
    events::{ServerUIEvent, UIServerEvent},
    server::{ServerOptions, ServerState},
};

pub struct SrsServer {
    pub tcp_server: Arc<Mutex<tcp_sync::SrsTcpServer>>,
    pub udp_server: Arc<Mutex<upd_voice::SrsVoiceServer>>,
    pub state: Arc<Mutex<ServerState>>,
    pub server_sender: Sender<UIServerEvent>,
    pub server_receiver: Receiver<ServerUIEvent>,
}

impl SrsServer {
    pub fn new(
        srs_server: tcp_sync::SrsTcpServer,
        voice_server: upd_voice::SrsVoiceServer,
        config: ServerOptions,
        server_sender: Sender<UIServerEvent>,
        server_receiver: Receiver<ServerUIEvent>,
    ) -> std::io::Result<Self> {
        Ok(Self {
            tcp_server: Arc::new(Mutex::new(srs_server)),
            udp_server: Arc::new(Mutex::new(voice_server)),
            server_sender,
            server_receiver,
            state: Arc::new(Mutex::new(ServerState {
                clients: HashMap::new(),
                options: config,
                version: crate::VERSION.to_owned(),
            })),
        })
    }

    pub fn start(&self) {
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
            // Server UI Channel Loop
            match self.server_receiver.recv() {
                Ok(event) => match event {
                    ServerUIEvent::BanClient(id) => {
                        log::info!("Banning client: {}", id);
                        break;
                    }
                    ServerUIEvent::UnbanClient(id) => {
                        log::info!("Unbanning client: {}", id);
                        break;
                    }
                    ServerUIEvent::Stop => {
                        log::info!("Shutting down server");
                        break;
                    }
                    ServerUIEvent::Start => {
                        log::info!("Starting server");
                        break;
                    }
                    ServerUIEvent::KickClient(id) => {
                        log::info!("Kicking client: {}", id);
                        break;
                    }
                    ServerUIEvent::MuteClient(id) => {
                        log::info!("Muting client: {}", id);
                        break;
                    }
                    ServerUIEvent::UnmuteClient(id) => {
                        log::info!("Unmuting client: {}", id);
                        break;
                    }
                },
                Err(e) => {
                    log::error!("Error receiving event: {}", e);
                    break;
                }
            }
        }
    }
}
