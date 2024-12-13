use crate::states::radio::{TcpMessageType, MESSAGE_TYPE_PARSE};
use crate::states::server::{ServerOptions, ServerState};
use crate::VERSION;
use serde_json::{self, Value};
use std::collections::HashMap;
use std::{
    io::Read,
    net::{TcpListener, TcpStream},
    sync::{Arc, Mutex},
    thread,
};

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
        for stream in self.listener.incoming() {
            let stream = stream.unwrap();
            let addr = stream.peer_addr().unwrap();
            println!("Connection from: {}", addr);
            let client = SrsClientLoop::new(stream.try_clone().unwrap(), Arc::clone(&self.state));
            let client = Arc::new(Mutex::new(client));
            self.connections.push(client.clone());
            thread::Builder::new()
                .name(format!("Client-{}", addr))
                .spawn(move || {
                    let mut client = client.lock().unwrap();
                    client.start();
                })
                .unwrap();
        }
    }
}

struct SrsClientLoop {
    stream: TcpStream,
    state: Arc<Mutex<ServerState>>,
}

impl SrsClientLoop {
    pub fn new(stream: TcpStream, state: Arc<Mutex<ServerState>>) -> Self {
        Self { stream, state }
    }

    pub fn start(&mut self) {
        let stream = self.stream.try_clone().unwrap();
        loop {
            if let Ok(message_str) = self.read_message() {
                if let Some(message_type) = SrsClientLoop::parse_message_type(&message_str) {
                    match message_type {
                        TcpMessageType::Update => {
                            println!("Update");
                        }
                        TcpMessageType::Ping => {
                            println!("Ping");
                        }
                        TcpMessageType::Sync => {
                            println!("Sync");
                        }
                        TcpMessageType::RadioUpdate => {
                            println!("RadioUpdate");
                        }
                        TcpMessageType::ServerSettings => {
                            println!("ServerSettings");
                        }
                        TcpMessageType::ClientDisconnect => {
                            println!("ClientDisconnect");
                        }
                        TcpMessageType::VersionMismatch => {
                            println!("VersionMismatch");
                        }
                        TcpMessageType::ClientPassword => {
                            println!("ClientPassword");
                        }
                        TcpMessageType::ClientAwacsDisconnect => {
                            println!("ClientAwacsDisconnect");
                        }
                    }
                } else {
                    eprintln!("Failed to get message type");
                    break;
                }
            } else {
                break;
            }
        }

        println!("Closing Connection: {}", stream.peer_addr().unwrap());
        stream.shutdown(std::net::Shutdown::Both).unwrap();
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
