use crate::network::utils::{get_sha256_hash, is_version_compatible};
use crate::states::client::Client;
use crate::states::radio::{
    LoginFailed, LoginRequest, LoginSuccess, LoginVersionMismatch, SrsClient, TcpMessageType,
    MESSAGE_TYPE_PARSE,
};
use crate::states::server::{ServerOptions, ServerState};
use crate::VERSION;
use log::{debug, info, warn};
use serde::Serialize;
use serde_json::{self, Value};
use std::collections::HashMap;
use std::io::Write;
use std::{
    io::Read,
    net::{TcpListener, TcpStream},
    sync::{Arc, Mutex},
    thread,
};
use uuid::Uuid;

pub struct SrsTcpServer {
    listener: TcpListener,
    connections: Vec<Arc<Mutex<SrsClientLoop>>>,
    state: Arc<Mutex<ServerState>>,
}

impl SrsTcpServer {
    pub fn new(address: &String, port: &u16) -> std::io::Result<Self> {
        let listener = TcpListener::bind(format!("{}:{}", address, port))?;
        Ok(Self {
            listener,
            state: Arc::new(Mutex::new(ServerState {
                clients: HashMap::new(),
                options: ServerOptions::default(),
                version: VERSION.to_owned(),
            })),
            connections: Vec::new(),
        })
    }

    pub fn start(&mut self, state: Arc<Mutex<ServerState>>) {
        self.state = state;
        info!(
            "TCP Server started on: {}",
            self.listener.local_addr().unwrap()
        );
        for stream in self.listener.incoming() {
            let stream = stream.unwrap();
            let addr = stream.peer_addr().unwrap();
            info!("TCP Connection from: {}", addr);
            let client = SrsClientLoop::new(stream.try_clone().unwrap(), Arc::clone(&self.state));
            let client = Arc::new(Mutex::new(client));
            self.connections.push(client.clone());
            let mut connections = self.connections.clone();
            thread::Builder::new()
                .name(format!("Client-{}", addr))
                .spawn(move || {
                    let mut client = client.lock().unwrap();
                    client.start(&mut connections);
                })
                .unwrap();
        }
    }
}

struct SrsClientLoop {
    stream: TcpStream,
    id: Option<String>,
    state: Arc<Mutex<ServerState>>,
    connections: Vec<Arc<Mutex<SrsClientLoop>>>,
}

impl SrsClientLoop {
    pub fn new(stream: TcpStream, state: Arc<Mutex<ServerState>>) -> Self {
        Self {
            stream,
            state,
            id: None,
            connections: Vec::new(),
        }
    }

    pub fn start(&mut self, connections: &mut Vec<Arc<Mutex<SrsClientLoop>>>) {
        self.connections = connections.clone();
        let stream = self.stream.try_clone().unwrap();
        loop {
            if let Ok(message_str) = self.read_message() {
                if message_str.is_empty() {
                    continue; // Skip empty messages
                }

                if let Some(message_type) = SrsClientLoop::parse_message_type(&message_str) {
                    match message_type {
                        TcpMessageType::Update => {
                            debug!("Update: {}", message_str);
                        }
                        TcpMessageType::Ping => {
                            debug!("Ping: {}", message_str);
                        }
                        TcpMessageType::Sync => {
                            debug!("Sync: {}", message_str);
                        }
                        TcpMessageType::RadioUpdate => {
                            debug!("RadioUpdate: {}", message_str);
                        }
                        TcpMessageType::ServerSettings => {
                            debug!("ServerSettings: {}", message_str);
                        }
                        TcpMessageType::ClientDisconnect => {
                            debug!("ClientDisconnect: {}", message_str);
                        }
                        TcpMessageType::VersionMismatch => {
                            debug!("VersionMismatch: {}", message_str); // This should not happen (Client only code)
                        }
                        TcpMessageType::Login => {
                            let login_data: LoginRequest =
                                serde_json::from_str(&message_str).unwrap();
                            if is_version_compatible(&login_data.version) {
                                let mut state = self.state.lock().unwrap();
                                if login_data.password
                                    == get_sha256_hash(&state.options.awacs.blue_password)
                                {
                                    drop(state);
                                    let message = LoginSuccess {
                                        version: VERSION.to_owned(),
                                        message_type: TcpMessageType::LoginSuccess as i32,
                                        client: SrsClient {
                                            name: login_data.client.name.clone(),
                                            coalition: 2, // For blue
                                            allow_record: login_data.client.allow_record,
                                            id: Uuid::new_v4().to_string(),
                                        },
                                    };
                                    self.send_message(&message).unwrap();
                                    let mut state = self.state.lock().unwrap();
                                    state.add_client(Client::new(
                                        message.client.id.clone(),
                                        stream.peer_addr().unwrap(),
                                    ));
                                    drop(state);
                                    self.id = Some(message.client.id.clone());
                                    info!("Login Success: {}", message.client.name);
                                } else if login_data.password
                                    == get_sha256_hash(&state.options.awacs.red_password)
                                {
                                    drop(state);
                                    let message = LoginSuccess {
                                        version: VERSION.to_owned(),
                                        message_type: TcpMessageType::LoginSuccess as i32,
                                        client: SrsClient {
                                            name: login_data.client.name.clone(),
                                            coalition: 1, // For red
                                            allow_record: login_data.client.allow_record,
                                            id: Uuid::new_v4().to_string(),
                                        },
                                    };
                                    self.send_message(&message).unwrap();
                                    let mut state = self.state.lock().unwrap();
                                    state.add_client(Client::new(
                                        message.client.id.clone(),
                                        stream.peer_addr().unwrap(),
                                    ));
                                    drop(state);
                                    self.id = Some(message.client.id.clone());
                                    info!("Login Success: {}", message.client.name);
                                } else {
                                    drop(state);
                                    debug!("Login Failed: {}", login_data.version);
                                    let message = LoginFailed {
                                        version: VERSION.to_owned(),
                                        message_type: TcpMessageType::LoginFailed as i32,
                                        message: "Invalid Password".to_owned(),
                                    };
                                    self.send_message(&message).unwrap();
                                    break;
                                }
                            } else {
                                debug!("Version Mismatch: {}", login_data.version);
                                let message = LoginVersionMismatch {
                                    version: VERSION.to_owned(),
                                    message_type: TcpMessageType::VersionMismatch as i32,
                                };
                                self.send_message(&message).unwrap();
                                break;
                            }
                        }
                        TcpMessageType::LoginSuccess => {
                            debug!("Login Success: {}", message_str); // This should not happen (Client only code)
                        }
                        TcpMessageType::LoginFailed => {
                            debug!("Login Failed: {}", message_str); // This should not happen (Client only code)
                        }
                    }
                } else {
                    warn!("Failed to get message type");
                    break;
                }
            } else {
                warn!("Failed to read message");
                break;
            }
        }

        info!("Closing Connection: {}", stream.peer_addr().unwrap());
        stream.shutdown(std::net::Shutdown::Both).unwrap();

        if let Some(id) = &self.id {
            let mut state = self.state.lock().unwrap();
            state.remove_client(id);
            drop(state);
        }
    }

    fn send_message<S: Serialize>(&self, message: &S) -> Result<(), std::io::Error> {
        let mut stream = self.stream.try_clone().unwrap();
        let message = serde_json::to_string(&message)? + "\n";
        stream.write_all(message.as_bytes())?;

        Ok(())
    }

    fn broadcast_message<S: Serialize>(&self, message: S) -> Result<(), std::io::Error> {
        for connection in &self.connections {
            let connection = connection.lock().unwrap();
            connection.send_message(&message)?;
        }
        Ok(())
    }

    fn read_message(&self) -> Result<String, std::io::Error> {
        let mut stream = self.stream.try_clone().unwrap();
        let mut message = String::new();
        let mut buf = [0; 1024];
        loop {
            let bytes_read = stream.read(&mut buf)?;
            if bytes_read == 0 {
                break;
            }
            message.push_str(&String::from_utf8_lossy(&buf[..bytes_read]));
            if bytes_read < buf.len() {
                break;
            }
        }
        Ok(message.trim_end().to_owned())
    }

    fn parse_message_type(message: &str) -> Option<&TcpMessageType> {
        let message: HashMap<String, Value> = match serde_json::from_str(message) {
            Ok(msg) => msg,
            Err(e) => {
                eprintln!("Failed to parse JSON: {}", e);
                return None;
            }
        };
        let message_type = match message.get("MsgType").and_then(|v| match v {
            Value::Number(s) => Some(format!("{}", s.as_u64().unwrap())),
            _ => None,
        }) {
            Some(mt) => mt,
            None => {
                eprintln!("Invalid message type");
                return None;
            }
        };
        let message_type = match MESSAGE_TYPE_PARSE.get(&message_type.as_str()) {
            Some(mt) => mt,
            None => {
                eprintln!("Unknown message type");
                return None;
            }
        };
        Some(message_type)
    }
}
