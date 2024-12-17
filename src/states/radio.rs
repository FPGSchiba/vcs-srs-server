use serde::{Deserialize, Serialize};
use std::str;

use phf::phf_map;

pub enum TcpMessageType {
    Update,
    Ping,
    Sync,
    RadioUpdate,
    ServerSettings,
    ClientDisconnect,
    VersionMismatch,
    Login,
    LoginSuccess,
    LoginFailed,
}

pub static MESSAGE_TYPE_PARSE: phf::Map<&'static str, TcpMessageType> = phf_map! {
    "0" => TcpMessageType::Update,
    "1" => TcpMessageType::Ping,
    "2" => TcpMessageType::Sync,
    "3" => TcpMessageType::RadioUpdate,
    "4" => TcpMessageType::ServerSettings,
    "5" => TcpMessageType::ClientDisconnect,
    "6" => TcpMessageType::VersionMismatch,
    "7" => TcpMessageType::Login,
    "8" => TcpMessageType::LoginSuccess,
    "9" => TcpMessageType::LoginFailed,
};

#[derive(Serialize, Deserialize, Debug)]
pub struct LoginClient {
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Coalition")]
    pub coalition: i32,
    #[serde(rename = "AllowRecord")]
    pub allow_record: bool,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct LoginRequest {
    #[serde(rename = "Client")]
    pub client: LoginClient,
    #[serde(rename = "Password")]
    pub password: String,
    #[serde(rename = "MsgType")]
    pub message_type: i32,
    #[serde(rename = "Version")]
    pub version: String,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct LoginVersionMismatch {
    #[serde(rename = "Version")]
    pub version: String,
    #[serde(rename = "MsgType")]
    pub message_type: i32,
}
