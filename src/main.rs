use network::SrsServer;
use states::server::ServerOptions;
use std::thread;

mod network;
mod states;

const VERSION: &str = env!("CARGO_PKG_VERSION");

fn main() -> std::io::Result<()> {
    let config = ServerOptions::from_config_file("server.toml").unwrap();
    let address = &config.server.server_ip;
    let port = &config.server.server_port;
    let srs_server = network::tcp_sync::SrsTcpServer::new(address, port).unwrap();
    let voice_server = network::upd_voice::SrsVoiceServer::new(address, port).unwrap();
    let server = SrsServer::new(srs_server, voice_server, config).unwrap();

    thread::Builder::new()
        .name("Server".to_string())
        .spawn(move || {
            server.start();
        })
        .unwrap();
    loop {} // TODO: Egui here
}
