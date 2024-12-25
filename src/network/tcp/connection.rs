use image::buffer;
use log::{debug, error, info};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{collections::HashMap, net::SocketAddr, sync::Arc};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::{broadcast, mpsc, RwLock},
};
use uuid::Uuid;

use crate::{
    error::{LoginError, ServerError},
    network::types::{
        ConnectionEvent, LoginFailed, LoginRequest, LoginSuccess, LoginVersionMismatch, SrsClient,
        TcpMessageType, MESSAGE_TYPE_PARSE,
    },
    state::SharedState,
    utils::network::{get_sha256_hash, is_version_compatible},
    VERSION,
};

pub struct ClientConnection {
    stream: TcpStream,
    addr: SocketAddr,
    state: Arc<RwLock<SharedState>>,
    event_tx: mpsc::Sender<ConnectionEvent>,
}

impl ClientConnection {
    pub fn new(
        stream: TcpStream,
        addr: SocketAddr,
        state: Arc<RwLock<SharedState>>,
        event_tx: mpsc::Sender<ConnectionEvent>,
    ) -> Self {
        Self {
            stream,
            addr,
            state,
            event_tx,
        }
    }

    pub async fn handle_connection(mut self) -> Result<(), ServerError> {
        loop {
            match self.read_message().await {
                Ok(message) => {
                    if message.is_empty() {
                        continue; // Skip empty messages
                    }
                    let _ = self.handle_message(&message).await;
                }
                Err(e) => {
                    error!("Failed to read message from {}: {}", self.addr, e);
                    break;
                }
            }
        }

        // Cleanup on disconnect
        self.handle_disconnect().await
    }

    async fn handle_message(&mut self, message: &String) -> Result<(), ServerError> {
        if let Some(message_type) = Self::parse_message_type(&message) {
            match message_type {
                TcpMessageType::Update => {
                    debug!("Update: {}", message);
                }
                TcpMessageType::Ping => {
                    debug!("Ping: {}", message);
                }
                TcpMessageType::Sync => {
                    debug!("Sync: {}", message);
                }
                TcpMessageType::RadioUpdate => {
                    debug!("RadioUpdate: {}", message);
                }
                TcpMessageType::ServerSettings => {
                    debug!("ServerSettings: {}", message);
                }
                TcpMessageType::ClientDisconnect => {
                    debug!("ClientDisconnect: {}", message);
                }
                TcpMessageType::Login => {
                    let login_data: LoginRequest = serde_json::from_str(&message).unwrap();
                    info!("Login: {:?}", login_data);
                    if let Err(error) = self.handle_login(&login_data).await {
                        match error {
                            LoginError::VersionMismatch => {
                                debug!("Version Mismatch: {}", login_data.version);
                                let message = LoginVersionMismatch {
                                    version: VERSION.to_owned(),
                                    message_type: TcpMessageType::VersionMismatch as i32,
                                };
                                self.send_message(&message).await.unwrap();
                            }
                            LoginError::InvalidPassword => {
                                debug!("Login Failed: {}", login_data.version);
                                let message = LoginFailed {
                                    version: VERSION.to_owned(),
                                    message_type: TcpMessageType::LoginFailed as i32,
                                    message: "Invalid Password".to_owned(),
                                };
                                self.send_message(&message).await.unwrap();
                            }
                        }
                    }
                }
                TcpMessageType::VersionMismatch => {
                    debug!("VersionMismatch: {}", message); // This should not happen (Client only code)
                }
                TcpMessageType::LoginSuccess => {
                    debug!("Login Success: {}", message); // This should not happen (Client only code)
                }
                TcpMessageType::LoginFailed => {
                    debug!("Login Failed: {}", message); // This should not happen (Client only code)
                }
            }
        } else {
            error!("Unknown message type: {}", message);
        }
        Ok(())
    }

    async fn handle_login(&mut self, login_data: &LoginRequest) -> Result<(), LoginError> {
        if !is_version_compatible(&login_data.version) {
            return Err(LoginError::VersionMismatch);
        }

        if login_data.password
            != get_sha256_hash(&self.state.read().await.options.awacs.blue_password)
            && login_data.password
                != get_sha256_hash(&self.state.read().await.options.awacs.red_password)
        {
            return Err(LoginError::InvalidPassword);
        }

        let is_blue = login_data.password
            == get_sha256_hash(&self.state.read().await.options.awacs.blue_password);
        let coalition = if is_blue { 2 } else { 1 };
        let message = LoginSuccess {
            version: VERSION.to_owned(),
            message_type: TcpMessageType::LoginSuccess as i32,
            client: SrsClient {
                name: login_data.client.name.clone(),
                coalition,
                allow_record: login_data.client.allow_record,
                id: Uuid::new_v4().to_string(),
            },
        };
        self.send_message(&message).await.unwrap();
        Ok(())
    }

    async fn send_message<S: Serialize>(&mut self, message: &S) -> Result<(), ServerError> {
        let data = serde_json::to_string(message)
            .map_err(|e| ServerError::ProtocolError(format!("Serialization error: {}", e)))?
            + "\n";

        self.stream
            .write_all(&data.into_bytes())
            .await
            .map_err(|e| ServerError::NetworkError(e))?;

        Ok(())
    }

    async fn read_message(&mut self) -> Result<String, std::io::Error> {
        let mut message = String::new();
        let mut buf = [0; 1024];
        loop {
            let bytes_read = self.stream.read(&mut buf).await.unwrap();
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

    async fn handle_disconnect(&mut self) -> Result<(), ServerError> {
        // Remove client from shared state
        // self.state.remove_client(self.addr).await?;

        // Notify other components about disconnection
        /*
        self.event_tx
            .send(ConnectionEvent::Disconnect(self.addr))
            .await
            .map_err(|e| ServerError::InternalError(format!("Failed to send event: {}", e)))?;
        */
        // Log disconnection
        self.stream.shutdown().await?;
        info!("Client {} disconnected", self.addr);

        Ok(())
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

// Helper trait for timeout operations
#[async_trait::async_trait]
impl crate::utils::Timeout for ClientConnection {
    async fn handle_timeout(&mut self) -> Result<(), ServerError> {
        // Send ping or check last activity
        // Implement timeout logic here
        Ok(())
    }
}
