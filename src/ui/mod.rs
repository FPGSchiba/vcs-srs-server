mod state;

use eframe::egui::CentralPanel;
use state::UIState;
use std::sync::mpsc::{Receiver, Sender};

use crate::states::events::{ServerUIEvent, UIServerEvent};

pub struct SrsUi {
    pub ui_sender: Sender<ServerUIEvent>,
    pub ui_receiver: Receiver<UIServerEvent>,
    pub state: UIState,
}

impl SrsUi {
    pub fn new(ui_sender: Sender<ServerUIEvent>, ui_receiver: Receiver<UIServerEvent>) -> Self {
        Self {
            ui_sender,
            ui_receiver,
            state: UIState::default(),
        }
    }
}

impl eframe::App for SrsUi {
    fn update(&mut self, ctx: &eframe::egui::Context, _frame: &mut eframe::Frame) {
        CentralPanel::default().show(ctx, |ui| {
            ui.label(format!("Num Clients: {}", self.state.clients.len()));
        });
    }
}
