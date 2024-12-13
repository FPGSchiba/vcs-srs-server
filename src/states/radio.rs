use phf::phf_map;

pub enum TcpMessageType {
    Update,
    Ping,
    Sync,
    RadioUpdate,
    ServerSettings,
    ClientDisconnect,
    VersionMismatch,
    ClientPassword,
    ClientAwacsDisconnect,
}

pub static MESSAGE_TYPE_PARSE: phf::Map<&'static str, TcpMessageType> = phf_map! {
    "0" => TcpMessageType::Update,
    "1" => TcpMessageType::Ping,
    "2" => TcpMessageType::Sync,
    "3" => TcpMessageType::RadioUpdate,
    "4" => TcpMessageType::ServerSettings,
    "5" => TcpMessageType::ClientDisconnect,
    "6" => TcpMessageType::VersionMismatch,
    "7" => TcpMessageType::ClientPassword,
    "8" => TcpMessageType::ClientAwacsDisconnect,
};
