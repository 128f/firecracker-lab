use std::sync::atomic::Ordering;

use crate::boot::mount::{MountConfig, mount_system_paths};
use crate::boot::time::restore_time;
use crate::stats::{Stats, Status};
use crate::transport::wire::{Ack, HealthStatus, RequestType, Response, ResponseType, StatusResponse, VmRestoreNotification};

pub fn dispatch(req: crate::transport::wire::Request, stats: &Stats) -> anyhow::Result<Response> {
    match req.request_type {
        Some(RequestType::Status(_status_req)) => Ok(Response {
            response_type: Some(ResponseType::Status(handle_status(stats))),
        }),
        Some(RequestType::VmRestore(notification)) => {
            handle_vm_restore(notification)?;
            Ok(Response {
                response_type: Some(ResponseType::Ack(Ack {})),
            })
        }
        None => anyhow::bail!("request with no request_type set"),
    }
}

fn handle_status(stats: &Stats) -> StatusResponse {
    let status = match stats.status.load(Ordering::Relaxed) {
        s if s == Status::Running as u8 => HealthStatus::Healthy,
        _ => HealthStatus::Degraded,
    };
    StatusResponse {
        status: status as i32,
        cpu_pct: stats.cpu_pct.load(Ordering::Relaxed) as u32,
        mem_available_bytes: stats.mem_available_bytes.load(Ordering::Relaxed),
        last_beat: stats.last_beat.load(Ordering::Relaxed),
    }
}

fn handle_vm_restore(notification: VmRestoreNotification) -> anyhow::Result<()> {
    mount_system_paths(MountConfig::default())?;
    restore_time(notification.restored_at)
}
