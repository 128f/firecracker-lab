mod status_api {
    include!(concat!(env!("OUT_DIR"), "/agent.rs"));
}
pub use status_api::{
    Ack, Error, HealthStatus, Request, Response, StartTcpVsockProxy, StatusResponse,
    StopTcpVsockProxy, VmRestoreNotification, request::RequestType, response::ResponseType,
};
