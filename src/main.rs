use log4rs;
use network::SrsServer;
use states::server::ServerOptions;
use std::thread;

mod network;
mod states;

const VERSION: &str = env!("CARGO_PKG_VERSION");

fn main() -> Result<(), eframe::Error> {
    log4rs::init_file("config/log4rs.yaml", Default::default()).unwrap();
    let config = ServerOptions::from_config_file("server.toml").unwrap();

    let address = &config.server.server_ip;
    let port = &config.server.server_port;
    let srs_server = network::tcp_sync::SrsTcpServer::new(address, port).unwrap();
    let voice_server = network::upd_voice::SrsVoiceServer::new(address, port).unwrap();
    let server = SrsServer::new(srs_server, voice_server, config).unwrap();
    let state = server.state.lock().unwrap().clone();

    thread::Builder::new()
        .name("Server".to_string())
        .spawn(move || {
            server.start();
        })
        .unwrap();

    let icon = include_bytes!("./resources/server-10.ico");
    let image = image::load_from_memory(icon)
        .expect("Failed to open icon path")
        .to_rgba8();
    let (icon_width, icon_height) = image.dimensions();

    let options = eframe::NativeOptions {
        viewport: eframe::egui::ViewportBuilder::default()
            .with_inner_size([330.0, 575.0])
            .with_resizable(false)
            .with_icon(egui::IconData {
                rgba: image.into_raw(),
                width: icon_width,
                height: icon_height,
            }),

        ..Default::default()
    };

    eframe::run_native(
        &format!("VNGD SRS Server - {}", VERSION),
        options,
        Box::new(|_| Ok(Box::new(state))),
    )
}
