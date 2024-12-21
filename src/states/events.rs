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
