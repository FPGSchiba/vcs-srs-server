use axum::{routing::get, Router};

async fn hello_world() -> &'static str {
    "Hello, World!"
}

pub fn get_router() -> Router {
    Router::new().route("/", get(hello_world))
}
