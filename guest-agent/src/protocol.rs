use std::sync::atomic::Ordering;

use crate::stats::{Stats, Status};

mod status_api {
    include!(concat!(env!("OUT_DIR"), "/agent.rs"));
}
pub use status_api::{HealthStatus, Request, StatusResponse, request::RequestType};

pub fn dispatch(req: status_api::Request, stats: &Stats) -> anyhow::Result<StatusResponse> {
    match req.request_type {
        Some(RequestType::Status(_status_req)) => handle_status(stats),
        None => anyhow::bail!("request with no request_type set"),
    }
}

fn handle_status(stats: &Stats) -> anyhow::Result<StatusResponse> {
    let status = match stats.status.load(Ordering::Relaxed) {
        s if s == Status::Running as u8 => HealthStatus::Healthy,
        _ => HealthStatus::Degraded,
    };
    Ok(StatusResponse {
        status: status as i32,
        cpu_pct: stats.cpu_pct.load(Ordering::Relaxed) as u32,
        mem_available_bytes: stats.mem_available_bytes.load(Ordering::Relaxed),
        last_beat: stats.last_beat.load(Ordering::Relaxed),
    })
}
