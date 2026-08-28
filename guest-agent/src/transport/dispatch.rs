use std::sync::atomic::Ordering;

use crate::boot::mount::{MountConfig, mount_system_paths};
use crate::boot::time::restore_time;
use crate::stats::{Stats, Status};
use crate::transport::tcp_vsock_proxy::ProxyRegistry;
use crate::transport::wire::{
    Ack, Error, HealthStatus, RequestType, Response, ResponseType, StartTcpVsockProxy,
    StatusResponse, StopTcpVsockProxy, VmRestoreNotification,
};

pub fn dispatch(
    req: crate::transport::wire::Request,
    stats: &Stats,
    proxy_registry: &ProxyRegistry,
) -> anyhow::Result<Response> {
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
        Some(RequestType::StartTcpVsockProxy(req)) => {
            Ok(ack_or_error(handle_start_proxy(req, proxy_registry)))
        }
        Some(RequestType::StopTcpVsockProxy(req)) => {
            Ok(ack_or_error(handle_stop_proxy(req, proxy_registry)))
        }
        None => anyhow::bail!("request with no request_type set"),
    }
}

fn ack_or_error(result: anyhow::Result<()>) -> Response {
    let response_type = match result {
        Ok(()) => ResponseType::Ack(Ack {}),
        Err(e) => ResponseType::Error(Error {
            reason: e.to_string(),
        }),
    };
    Response {
        response_type: Some(response_type),
    }
}

fn handle_start_proxy(
    req: StartTcpVsockProxy,
    proxy_registry: &ProxyRegistry,
) -> anyhow::Result<()> {
    proxy_registry.start(parse_port(req.tcp_port)?, req.cid, req.vsock_port)
}

fn handle_stop_proxy(req: StopTcpVsockProxy, proxy_registry: &ProxyRegistry) -> anyhow::Result<()> {
    proxy_registry.stop(parse_port(req.tcp_port)?)
}

fn parse_port(port: u32) -> anyhow::Result<u16> {
    u16::try_from(port).map_err(|_| anyhow::anyhow!("invalid tcp_port {port}: must be 1-65535"))
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
