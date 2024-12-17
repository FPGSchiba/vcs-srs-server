use eframe::egui::CentralPanel;
use std::sync::{Arc, Mutex};

use crate::states::server::ServerState;

pub struct SrsUi {
    pub server_state: Arc<Mutex<ServerState>>,
}

impl SrsUi {
    pub fn new(server_state: Arc<Mutex<ServerState>>) -> Self {
        Self { server_state }
    }
}

impl eframe::App for SrsUi {
    fn update(&mut self, ctx: &eframe::egui::Context, _frame: &mut eframe::Frame) {
        CentralPanel::default().show(ctx, |ui| {
            let server_state = self.server_state.try_lock();
            if let Ok(server_state) = server_state {
                ui.label(format!("Num Clients: {}", server_state.clients.len()));
            } else {
                ui.label("Num Clients: 0");
            }
        });
    }
}
