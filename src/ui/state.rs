use std::collections::HashMap;

pub struct UIClient {
    pub name: String,
    pub coalition: i32,
}

pub struct UIState {
    pub clients: HashMap<String, UIClient>,
}

impl Default for UIState {
    fn default() -> Self {
        Self {
            clients: HashMap::new(),
        }
    }
}
