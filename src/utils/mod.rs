pub mod network;

use crate::error::ServerError;
use async_trait::async_trait;
use std::{future::Future, time::Duration};
use tokio::time;

#[async_trait]
pub trait Timeout {
    /// Called when a timeout occurs
    async fn handle_timeout(&mut self) -> Result<(), ServerError>;

    /// Run an operation with timeout
    async fn run_with_timeout<F, T>(
        &mut self,
        duration: Duration,
        operation: F,
    ) -> Result<Option<T>, ServerError>
    where
        F: Future<Output = Result<T, ServerError>> + Send + 'static,
        T: Send + 'static,
    {
        tokio::select! {
            result = operation => {
                result.map(Some)
            }
            _ = time::sleep(duration) => {
                self.handle_timeout().await?;
                Ok(None)
            }
        }
    }

    /// Check if operation has timed out
    async fn check_timeout(
        &mut self,
        last_activity: std::time::Instant,
        timeout: Duration,
    ) -> Result<bool, ServerError> {
        if last_activity.elapsed() > timeout {
            self.handle_timeout().await?;
            Ok(true)
        } else {
            Ok(false)
        }
    }
}
