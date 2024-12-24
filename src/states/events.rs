pub enum UIServerEvent {
    AddClient(String),
    RemoveClient(String),
}

pub enum ServerUIEvent {
    BanClient(String),
    KickClient(String),
    UnbanClient(String),
    MuteClient(String),
    UnmuteClient(String),
    Stop,
    Start,
}

pub enum TCPServerEvent {
    // From Control (TCP) component to the Server component
    ClientConnected(String),
    ClientDisconnected(String),
    BanClient(String),
    KickClient(String),
    UnbanClient(String),
    MuteClient(String),
    UnmuteClient(String),
}

pub enum ClientTCPEvent {
    // From Client Loop to the Control (TCP) component
    ClientConnected(String),
    ClientDisconnected(String),
    BanClient(String),
    KickClient(String),
    UnbanClient(String),
    MuteClient(String),
    UnmuteClient(String),
}

pub enum TCPClientEvent {
    // From Control (TCP) component to the Client Loop
    BanClient(String),
    KickClient(String),
    UnbanClient(String),
    MuteClient(String),
    UnmuteClient(String),
}
