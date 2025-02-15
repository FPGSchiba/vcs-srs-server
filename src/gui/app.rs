use std::cmp::PartialEq;
use eframe::{App, Frame};
use log::error;
use std::sync::Arc;
use egui::{Color32, RichText};
use egui_dock::{DockArea, DockState, NodeIndex, Style, SurfaceIndex};
use tokio::{
    runtime::Runtime,
    sync::{broadcast, mpsc, RwLock},
};

use crate::{
    event::{ServerToUiEvent, UiToServerEvent},
    ControlMessage,
};
use crate::state::{AdminState, ClientState, OptionsState};

pub struct ServerGui {
    client_state: Arc<RwLock<ClientState>>,
    options_state: Arc<RwLock<OptionsState>>,
    admin_state: Arc<RwLock<AdminState>>,
    ui_tx: mpsc::Sender<UiToServerEvent>,
    server_running: bool,
    dock_state: DockState<ServerTab>,
    counter: usize,
}

#[derive(PartialEq)]
enum TabKind {
    Settings,
    ClientList,
    Admin,
}

struct ServerTab {
    kind: TabKind,
    surface: SurfaceIndex,
    node: NodeIndex,
}

impl ServerTab {
    fn setting(surface: SurfaceIndex, node: NodeIndex) -> Self {
        Self {
            kind: TabKind::Settings,
            surface,
            node,
        }
    }

    fn client_list(surface: SurfaceIndex, node: NodeIndex) -> Self {
        Self {
            kind: TabKind::ClientList,
            surface,
            node,
        }
    }

    fn admin(surface: SurfaceIndex, node: NodeIndex) -> Self {
        Self {
            kind: TabKind::Admin,
            surface,
            node,
        }
    }

    fn title(&self) -> String {
        match self.kind {
            TabKind::Settings => format!("Settings Tab {}", self.node.0),
            TabKind::ClientList => format!("Client List Tab {}", self.node.0),
            TabKind::Admin => format!("Admin Tab {}", self.node.0),
        }
    }

    fn update(&self, ui: &mut egui::Ui) {
        ui.label(match self.kind {
            TabKind::Settings => {
                RichText::new(format!("Content of {}. This tab is ho-hum.", self.title()))
            }
            TabKind::ClientList => RichText::new(format!(
                "Content of {}. This tab sure is fancy!",
                self.title()
            ))
                .italics()
                .size(20.0)
                .color(Color32::from_rgb(255, 128, 64)),
            TabKind::Admin => RichText::new(format!(
                "Content of {}. This tab is for the admin.",
                self.title()
            ))
                .code()
                .size(24.0)
                .color(Color32::from_rgb(128, 255, 64)),
        });
    }
}

struct TabViewer<'a> {
    added_nodes: &'a mut Vec<ServerTab>,
}

impl egui_dock::TabViewer for TabViewer<'_> {
    type Tab = ServerTab;

    fn title(&mut self, tab: &mut Self::Tab) -> egui::WidgetText {
        tab.title().into()
    }

    fn ui(&mut self, ui: &mut egui::Ui, tab: &mut Self::Tab) {
        tab.update(ui);
    }

    fn closeable(&mut self, tab: &mut Self::Tab) -> bool {
        self.added_nodes.iter().any(|t| t.kind == tab.kind)
    }

    fn add_popup(&mut self, ui: &mut egui::Ui, surface: SurfaceIndex, node: NodeIndex) {
        ui.set_min_width(120.0);
        ui.style_mut().visuals.button_frame = false;

        if ui.button("Settings tab").clicked() {
            self.added_nodes.push(ServerTab::setting(surface, node));
        }

        if ui.button("Client List tab").clicked() {
            self.added_nodes.push(ServerTab::client_list(surface, node));
        }

        if ui.button("Admin tab").clicked() {
            self.added_nodes.push(ServerTab::admin(surface, node));
        }
    }
}

impl ServerGui {
    pub fn new(
        client_state: Arc<RwLock<ClientState>>,
        options_state: Arc<RwLock<OptionsState>>,
        admin_state: Arc<RwLock<AdminState>>,
        ui_tx: mpsc::Sender<UiToServerEvent>,
        mut server_rx: broadcast::Receiver<ServerToUiEvent>,
    ) -> Self {
        let mut tree = DockState::new(vec![ServerTab::setting(SurfaceIndex::main(), NodeIndex(1))]);

        let [_, _] = tree.main_surface_mut().split_left(
            NodeIndex::root(),
            0.5,
            vec![ServerTab::client_list(SurfaceIndex::main(), NodeIndex(2))],
        );

        // You can modify the tree before constructing the dock
        let [_, _] = tree.main_surface_mut().split_below(
            NodeIndex::root(),
            0.6,
            vec![ServerTab::admin(SurfaceIndex::main(), NodeIndex(3))],
        );

        Self {
            client_state,
            options_state,
            admin_state,
            ui_tx,
            server_running: true,
            dock_state: tree,
            counter: 3,
        }
    }
}

impl eframe::App for ServerGui {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        let mut added_nodes = Vec::new();
        DockArea::new(&mut self.dock_state)
            .show_add_buttons(true)
            .show_add_popup(true)
            .style(Style::from_egui(ctx.style().as_ref()))
            .show(
                ctx,
                &mut TabViewer {
                    added_nodes: &mut added_nodes,
                },
            );

        added_nodes.drain(..).for_each(|node| {
            self.dock_state
                .set_focused_node_and_surface((node.surface, node.node));
            self.dock_state.push_to_focused_leaf(ServerTab {
                kind: node.kind,
                surface: node.surface,
                node: NodeIndex(self.counter),
            });
            self.counter += 1;
        });
    }
}
