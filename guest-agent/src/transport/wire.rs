mod status_api {
    include!(concat!(env!("OUT_DIR"), "/agent.rs"));
}
pub use status_api::{HealthStatus, Request, StatusResponse, request::RequestType};
