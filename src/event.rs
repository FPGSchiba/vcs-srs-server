use tokio::sync::{broadcast, mpsc};

// Events from UI to Server
#[derive(Debug, Clone)]
pub enum UiToVoiceEvent {
    // Add more events as needed
}

#[derive(Debug, Clone)]
pub enum UiToControlEvent {
    // Add more events as needed
    BanClient { client_id: String },
    UnbanClient { client_id: String },
    KickClient { client_id: String },
    MuteClient { client_id: String },
    UnmuteClient { client_id: String },
}

// Events from Server to UI
#[derive(Debug, Clone)]
pub enum VoiceToUiEvent {
    Error { message: String },
    // Add more events as needed
}

#[derive(Debug, Clone)]
pub enum ControlToUiEvent {
    ClientConnected { client_id: String },
    ClientDisconnected { client_id: String },
    Error { message: String },
    // Add more events as needed
}

#[derive(Debug, Clone)]
pub enum ControlToVoiceEvent {
    ClientConnected { client_id: String },
    ClientDisconnected { client_id: String },
    MuteClient { client_id: String },
    UnmuteClient { client_id: String },
    // Add more events as needed
}

pub struct EventBus {
    pub ui_voice_tx: mpsc::Sender<UiToVoiceEvent>,
    pub ui_voice_rx: mpsc::Receiver<UiToVoiceEvent>,
    pub ui_control_tx: mpsc::Sender<UiToControlEvent>,
    pub ui_control_rx: mpsc::Receiver<UiToControlEvent>,
    pub voice_ui_tx: broadcast::Sender<VoiceToUiEvent>,
    pub voice_ui_rx: broadcast::Receiver<VoiceToUiEvent>,
    pub control_ui_tx: broadcast::Sender<ControlToUiEvent>,
    pub control_ui_rx: broadcast::Receiver<ControlToUiEvent>,
    pub control_voice_tx: broadcast::Sender<ControlToVoiceEvent>,
    pub control_voice_rx: broadcast::Receiver<ControlToVoiceEvent>,
}

impl EventBus {
    pub fn new() -> Self {
        let (ui_voice_tx, ui_voice_rx) = mpsc::channel(100);
        let (ui_control_tx, ui_control_rx) = mpsc::channel(100);
        let (voice_ui_tx, voice_ui_rx) = broadcast::channel(100);
        let (control_ui_tx, control_ui_rx) = broadcast::channel(100);
        let (control_voice_tx, control_voice_rx) = broadcast::channel(100);
        Self {
            ui_voice_tx,
            ui_voice_rx,
            ui_control_tx,
            ui_control_rx,
            voice_ui_tx,
            voice_ui_rx,
            control_ui_tx,
            control_ui_rx,
            control_voice_tx,
            control_voice_rx,
        }
    }
}
