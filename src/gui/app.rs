use eframe::{App, Frame};
use log::error;
use std::sync::Arc;
use tokio::{
    runtime::Runtime,
    sync::{broadcast, mpsc, RwLock},
};

use crate::{
    event::{ServerToUiEvent, UiToServerEvent},
    state::SharedState,
    ControlMessage,
};

pub struct ServerGui {
    state: Arc<RwLock<SharedState>>,
    ui_tx: mpsc::Sender<UiToServerEvent>,
    server_running: bool,
}

impl ServerGui {
    pub fn new(
        state: Arc<RwLock<SharedState>>,
        ui_tx: mpsc::Sender<UiToServerEvent>,
        mut server_rx: broadcast::Receiver<ServerToUiEvent>,
    ) -> Self {
        // Spawn a task to handle server events
        let state_clone = state.clone();
        tokio::spawn(async move {
            while let Ok(event) = server_rx.recv().await {
                match event {
                    ServerToUiEvent::ClientConnected { client_id } => {
                        // Update UI state
                    }
                    ServerToUiEvent::ClientDisconnected { client_id } => {
                        // Update UI state
                    }
                    ServerToUiEvent::Error { message } => {
                        // Update UI state
                    }
                }
            }
        });

        Self {
            state,
            ui_tx,
            server_running: true,
        }
    }

    fn send_event(&self, event: UiToServerEvent) {
        if let Err(e) = self.ui_tx.try_send(event) {
            error!("Failed to send event to server: {}", e);
        }
    }
}

impl eframe::App for ServerGui {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        egui::CentralPanel::default().show(ctx, |ui| {
            ui.heading("VCS SRS Server");

            ui.horizontal(|ui| {
                ui.label("Server Status:");
                ui.label(if self.server_running {
                    "Running"
                } else {
                    "Stopped"
                });
            });

            ui.separator();
            ui.horizontal(|ui| {
                ui.label("Clients Connected:");
                ui.label(self.state.try_read().unwrap().clients.len().to_string());
            });
        });
    }
}
