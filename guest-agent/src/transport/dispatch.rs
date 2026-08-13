use std::sync::atomic::Ordering;

use crate::stats::{Stats, Status};
use crate::transport::wire::{HealthStatus, RequestType, StatusResponse};

pub fn dispatch(req: crate::transport::wire::Request, stats: &Stats) -> anyhow::Result<StatusResponse> {
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
