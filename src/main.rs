use log::info;
use log4rs;
use network::SrsServer;
use states::server::{self, ServerOptions};
use std::{
    sync::{Arc, Mutex},
    thread,
};
use ui::SrsUi;

mod network;
mod states;
mod ui;

const VERSION: &str = env!("CARGO_PKG_VERSION");

fn main() -> Result<(), eframe::Error> {
    log4rs::init_file("config/log4rs.yaml", Default::default()).unwrap();
    let config = ServerOptions::from_config_file("config/server.toml").unwrap();

    info!("Starting VNGD SRS Server - v{}", VERSION);

    let address = &config.server.server_ip;
    let port = &config.server.server_port;
    let srs_server = network::tcp_sync::SrsTcpServer::new(address, port).unwrap();
    let voice_server = network::upd_voice::SrsVoiceServer::new(address, port).unwrap();
    let server = Arc::new(Mutex::new(
        SrsServer::new(srs_server, voice_server, config).unwrap(),
    ));

    let server_clone = Arc::clone(&server);
    let state = Arc::clone(&server.lock().unwrap().state);
    thread::Builder::new()
        .name("Server".to_string())
        .spawn(move || {
            let srs_server = server_clone.lock().unwrap();
            srs_server.start();
        })
        .unwrap();

    let icon = include_bytes!("resources/server-10.ico");
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
        Box::new(|_| Ok(Box::new(SrsUi::new(state)))),
    )
}
