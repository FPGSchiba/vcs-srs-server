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
    // ... other error types
}

pub enum LoginError {
    VersionMismatch,
    InvalidPassword,
}
