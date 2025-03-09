use log::{debug, error, info};
use serde::Serialize;
use serde_json::Value;
use std::{
    collections::HashMap,
    net::SocketAddr,
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::{mpsc, RwLock},
};
use uuid::Uuid;

use crate::network::types::RadioUpdateRequest;
use crate::state::client::Client;
use crate::state::{AdminState, ClientState, OptionsState};
use crate::{
    error::{LoginError, ServerError},
    network::types::{
        ConnectionEvent, LoginFailed, LoginRequest, LoginSuccess, LoginVersionMismatch,
        TcpMessageType, MESSAGE_TYPE_PARSE,
    },
    utils::network::{get_sha256_hash, is_version_compatible},
    VERSION,
};

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
        let mut is_disconnected = false;
        let timeout_duration = Duration::from_secs(30); // Configurable timeout
        let mut last_activity = Instant::now();

        loop {
            if is_disconnected {
                break;
            }

            // Check for timeout
            if last_activity.elapsed() > timeout_duration {
                error!("Connection timeout for client {}", self.addr);
                return Err(ServerError::NetworkError(std::io::Error::new(
                    std::io::ErrorKind::TimedOut,
                    "Connection timeout",
                )));
            }

            // Use tokio::select! to handle both message reading and timeout
            tokio::select! {
                read_result = self.read_message() => {
                    match read_result {
                        Ok(message) => {
                            last_activity = Instant::now(); // Reset timeout counter
                            if message.is_empty() {
                                continue;
                            }

                            if let Err(e) = self.handle_message(&message).await {
                                error!("Failed to handle message from {}: {}", self.addr, e);
                                match self.handle_disconnect().await {
                                    Ok(_) => {
                                        info!("Client {} disconnected gracefully after message handling error", self.addr);
                                        is_disconnected = true;
                                    }
                                    Err(disconnect_err) => {
                                        error!("Failed to disconnect client {} gracefully: {}", self.addr, disconnect_err);
                                        is_disconnected = true;
                                        return Err(e);
                                    }
                                }
                            }
                        }
                        Err(e) => {
                            error!("Failed to read message from {}: {}", self.addr, e);
                            let server_error = ServerError::NetworkError(e);

                            if let Err(disconnect_err) = self.handle_disconnect().await {
                                error!("Failed to disconnect client {} after read error: {}", self.addr, disconnect_err);
                                return Err(server_error);
                            }

                            info!("Client {} disconnected after read error", self.addr);
                            break;
                        }
                    }
                }
                _ = tokio::time::sleep(timeout_duration) => {
                    error!("Connection timeout for client {}", self.addr);
                    if let Err(e) = self.handle_disconnect().await {
                        error!("Failed to disconnect client {} after timeout: {}", self.addr, e);
                    }
                    return Err(ServerError::NetworkError(
                        std::io::Error::new(std::io::ErrorKind::TimedOut, "Connection timeout")
                    ));
                }
            }
        }

        if !is_disconnected {
            self.handle_disconnect().await?;
        }

        Ok(())
    }

    async fn handle_message(&mut self, message: &String) -> Result<(), ServerError> {
        let message_type = Self::parse_message_type(message)?;

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
                let radio_data: RadioUpdateRequest =
                    serde_json::from_str(message).map_err(|e| {
                        ServerError::ProtocolError(format!("Invalid radio update format: {}", e))
                    })?;

                let radio_info = radio_data.client.radio_information.ok_or_else(|| {
                    ServerError::ProtocolError("Missing radio information".to_string())
                })?;

                self.client_state
                    .write()
                    .await
                    .update_radio_information(&self.addr, radio_info)
                    .map_err(|e| {
                        ServerError::StateError(format!("Failed to update radio info: {}", e))
                    })?;

                info!("Updated Radio information for: {}", self.addr);
            }
            TcpMessageType::ServerSettings => {
                debug!("ServerSettings: {}", message);
            }
            TcpMessageType::ClientDisconnect => {
                self.handle_disconnect().await?;
            }
            TcpMessageType::Login => {
                let login_data: LoginRequest = serde_json::from_str(message).map_err(|e| {
                    ServerError::ProtocolError(format!("Invalid login request format: {}", e))
                })?;

                info!("Login attempt from client: {}", login_data.client.name);

                match self.handle_login(&login_data).await {
                    Ok(_) => {
                        info!("Login successful for client: {}", login_data.client.name);
                    }
                    Err(error) => {
                        let response = match error {
                            LoginError::VersionMismatch => {
                                debug!("Version mismatch: client version {}", login_data.version);
                                let message = LoginVersionMismatch {
                                    version: VERSION.to_owned(),
                                    message_type: TcpMessageType::VersionMismatch as i32,
                                };
                                let result = self.send_message(&message).await;
                                if let Err(e) = result {
                                    error!("Failed to send version mismatch response: {}", e);
                                    return Err(ServerError::NetworkError(std::io::Error::new(
                                        std::io::ErrorKind::Other,
                                        "Failed to send version mismatch response",
                                    )));
                                }
                            }
                            LoginError::InvalidPassword => {
                                debug!("Invalid password for client: {}", login_data.client.name);
                                let message = LoginFailed {
                                    version: VERSION.to_owned(),
                                    message_type: TcpMessageType::LoginFailed as i32,
                                    message: "Invalid Password".to_owned(),
                                };
                                let result = self.send_message(&message).await;
                                if let Err(e) = result {
                                    error!("Failed to send login response: {}", e);
                                    return Err(ServerError::NetworkError(std::io::Error::new(
                                        std::io::ErrorKind::Other,
                                        "Failed to send login response",
                                    )));
                                }
                            }
                        };

                        self.send_message(&response).await.map_err(|e| {
                            ServerError::NetworkError(std::io::Error::new(
                                std::io::ErrorKind::Other,
                                format!("Failed to send login response: {}", e),
                            ))
                        })?;

                        return Err(ServerError::ProtocolError(format!(
                            "Login failed: {}",
                            match error {
                                LoginError::VersionMismatch => "version mismatch",
                                LoginError::InvalidPassword => "invalid password",
                            }
                        )));
                    }
                };
            }
            TcpMessageType::VersionMismatch
            | TcpMessageType::LoginSuccess
            | TcpMessageType::LoginFailed => {
                debug!(
                    "Received client-only message type {:?} from {}: {}",
                    message_type, self.addr, message
                );
                return Err(ServerError::ProtocolError(format!(
                    "Received client-only message type from {}",
                    self.addr
                )));
            }
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
        self.client_state.write().await.remove_client(&self.addr);
        if let Err(e) = self.stream.shutdown().await {
            return Err(ServerError::NetworkError(e));
        }
        info!("Client {} disconnected", self.addr);
        Ok(())
    }

    fn parse_message_type(message: &str) -> Result<TcpMessageType, ServerError> {
        let message: HashMap<String, Value> = serde_json::from_str(message)
            .map_err(|e| ServerError::ProtocolError(format!("Invalid JSON: {}", e)))?;

        let message_type = message
            .get("MsgType")
            .and_then(|v| match v {
                Value::Number(s) => s.as_u64().map(|n| n.to_string()),
                _ => None,
            })
            .ok_or_else(|| {
                ServerError::ProtocolError("Missing or invalid message type".to_string())
            })?;

        MESSAGE_TYPE_PARSE
            .get(message_type.as_str())
            .cloned()
            .ok_or_else(|| {
                ServerError::ProtocolError(format!("Unknown message type: {}", message_type))
            })
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
