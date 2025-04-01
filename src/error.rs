use std::io;

use thiserror::Error;

#[derive(Debug, Error)]
pub enum ServerError {
    #[error("Network error: {0}")]
    NetworkError(#[from] std::io::Error),

    #[error("State error: {0}")]
    StateError(String),

    #[error("Internal error: {0}")]
    InternalError(String),

    #[error("Protocol error: {0}")]
    ProtocolError(String),

    #[error("JSON parsing error: {0}")]
    JsonError(#[from] serde_json::Error),

    #[error("Configuration error: {0}")]
    ConfigError(String),

    #[error("Message type error: {0}")]
    MessageTypeError(String),

    #[error("Client error: {0}")]
    ClientError(String),
}

#[derive(Error, Debug)]
pub enum VoiceServerError {
    #[error("Network error: {0}")]
    NetworkError(#[from] std::io::Error),
    #[error("Server initialization error: {0}")]
    InitError(String),
    #[error("Event handling error: {0}")]
    EventError(String),
    #[error("State error: {0}")]
    StateError(String),
    #[error("Handler error: {0}")]
    HandlerError(String),
}

#[derive(Error, Debug)]
pub enum ControlError {
    #[error("Network error: {0}")]
    NetworkError(#[from] std::io::Error),
    #[error("Server initialization error: {0}")]
    InitError(String),
    #[error("Event handling error: {0}")]
    EventError(String),
    #[error("State error: {0}")]
    StateError(String),
    #[error("Handler error: {0}")]
    HandlerError(String),
}

#[derive(Debug, Error)]
pub enum LoginError {
    #[error("Version mismatch")]
    VersionMismatch,
    #[error("Invalid password")]
    InvalidPassword,
}

#[derive(Error, Debug)]
pub enum ConfigError {
    #[error("IO error: {0}")]
    IoError(#[from] io::Error),

    #[error("TOML serialization error: {0}")]
    TomlSerError(#[from] toml::ser::Error),

    #[error("TOML deserialization error: {0}")]
    TomlDeError(#[from] toml::de::Error),

    #[error("Validation error: {0}")]
    ValidationError(String),
}

impl From<LoginError> for ServerError {
    fn from(err: LoginError) -> Self {
        match err {
            LoginError::VersionMismatch => {
                ServerError::ProtocolError("Version mismatch".to_string())
            }
            LoginError::InvalidPassword => {
                ServerError::ProtocolError("Invalid password".to_string())
            }
        }
    }
}

impl From<ConfigError> for ServerError {
    fn from(err: ConfigError) -> Self {
        ServerError::ConfigError(err.to_string())
    }
}
