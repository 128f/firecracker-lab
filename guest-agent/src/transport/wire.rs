mod status_api {
    include!(concat!(env!("OUT_DIR"), "/agent.rs"));
}
pub use status_api::{
    Ack, HealthStatus, Request, Response, StatusResponse, VmRestoreNotification,
    request::RequestType, response::ResponseType,
};
