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
        ConnectionEvent, LoginFailed, LoginRequest, LoginSuccess, LoginVersionMismatch,
        TcpMessageType, MESSAGE_TYPE_PARSE,
    },
    utils::network::{get_sha256_hash, is_version_compatible},
    VERSION,
};
use crate::network::types::RadioUpdateRequest;
use crate::state::client::Client;
use crate::state::{AdminState, ClientState, OptionsState};

pub struct ClientConnection {
    stream: TcpStream,
    addr: SocketAddr,
    client_state: Arc<RwLock<ClientState>>,
    options_state: Arc<RwLock<OptionsState>>,
    admin_state: Arc<RwLock<AdminState>>,
    event_tx: mpsc::Sender<ConnectionEvent>,
}

impl ClientConnection {
    pub fn new(
        stream: TcpStream,
        addr: SocketAddr,
        client_sate: Arc<RwLock<ClientState>>,
        options_state: Arc<RwLock<OptionsState>>,
        admin_state: Arc<RwLock<AdminState>>,
        event_tx: mpsc::Sender<ConnectionEvent>,
    ) -> Self {
        Self {
            stream,
            addr,
            client_state: client_sate,
            options_state,
            admin_state,
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
                    if let Err(e) = self.handle_message(&message).await {
                        error!("Failed to handle message from {}: {}", self.addr, e);
                        if let Err(e) = self.handle_disconnect().await {
                            panic!("Failed to disconnect client: {}", e);
                        }
                        break;
                    }
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
                    let radio_data: RadioUpdateRequest = serde_json::from_str(&message).unwrap();
                    self.client_state.write().await.update_radio_information(&self.addr, radio_data.client.radio_information.unwrap());
                    info!("Updated Radio information for: {}", self.addr);
                }
                TcpMessageType::ServerSettings => {
                    debug!("ServerSettings: {}", message);
                }
                TcpMessageType::ClientDisconnect => {
                    self.handle_disconnect().await?;
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
            return Err(ServerError::ProtocolError(format!("Unknown message type: {}", message).to_owned()));
        }
        Ok(())
    }

    async fn handle_login(&mut self, login_data: &LoginRequest) -> Result<(), LoginError> {
        if !is_version_compatible(&login_data.version) {
            return Err(LoginError::VersionMismatch);
        }

        if login_data.password
            != get_sha256_hash(&self.options_state.read().await.options.awacs.blue_password)
            && login_data.password
                != get_sha256_hash(&self.options_state.read().await.options.awacs.red_password)
        {
            return Err(LoginError::InvalidPassword);
        }

        let is_blue = login_data.password
            == get_sha256_hash(&self.options_state.read().await.options.awacs.blue_password);
        let coalition = if is_blue { 2 } else { 1 };
        let client_id = Uuid::new_v4().to_string();
        let message = LoginSuccess {
            version: VERSION.to_owned(),
            message_type: TcpMessageType::LoginSuccess as i32,
            client: Client {
                addr: Some(self.addr),
                name: login_data.client.name.clone(),
                coalition,
                allow_record: login_data.client.allow_record,
                id: Some(client_id.clone()),
                radio_information: None,
            },
        };
        self.send_message(&message).await.unwrap();
        self.client_state.write().await.add_client(
            self.addr,
            Client {
                id: Some(client_id),
                addr: Some(self.addr),
                name: login_data.client.name.clone(),
                coalition,
                allow_record: login_data.client.allow_record,
                radio_information: None,
            },
        );
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
            let bytes_read = self.stream.read(&mut buf).await?;
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
        self.client_state.write().await.remove_client(&self.addr);
        // Shutdown the stream
        self.stream.shutdown().await?;
        // Log disconnection
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
