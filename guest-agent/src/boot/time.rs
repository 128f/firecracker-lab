use rustix::time::{ClockId, Timespec, clock_settime};

/// Sets the system clock to `restored_at_secs` (unix epoch seconds).
/// Used to correct the guest clock after a snapshot restore, where the
/// VM resumes with a stale time from the moment it was paused.
pub fn restore_time(restored_at_secs: u64) -> anyhow::Result<()> {
    clock_settime(
        ClockId::Realtime,
        Timespec {
            tv_sec: restored_at_secs as i64,
            tv_nsec: 0,
        },
    )
    .map_err(|e| anyhow::anyhow!("failed to set clock: {e}"))
}
