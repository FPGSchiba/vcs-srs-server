use log::{error, info};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use socketioxide::extract::{Data, SocketRef};

#[derive(Serialize, Deserialize, Debug)]
pub struct AuthData {
    #[serde(rename = "Username")]
    username: String,
    #[serde(rename = "Password")]
    password: String,
}

pub fn on_connect(socket: SocketRef, Data(data): Data<AuthData>) {
    info!(
        "Socket.IO connected: {:?} {:?} {:?}",
        socket.ns(),
        socket.id,
        data
    );

    // Handle disconnect
    socket.on_disconnect(move |socket: SocketRef| {
        info!("Socket.IO disconnected: {:?} {:?}", socket.ns(), socket.id);
    });
}
