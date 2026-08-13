use rustix::io::read;
use rustix::time::{
    Itimerspec, TimerfdClockId, TimerfdFlags, TimerfdTimerFlags, Timespec, timerfd_create,
    timerfd_settime,
};
use std::os::fd::{BorrowedFd, OwnedFd};
use std::time::Duration;

/// Creates a periodic CLOCK_MONOTONIC timerfd that fires every `interval`.
pub fn connect_timerfd(interval: Duration) -> anyhow::Result<OwnedFd> {
    let fd = timerfd_create(TimerfdClockId::Monotonic, TimerfdFlags::CLOEXEC)?;
    let ts = Timespec {
        tv_sec: interval.as_secs() as i64,
        tv_nsec: interval.subsec_nanos() as i64,
    };
    timerfd_settime(
        &fd,
        TimerfdTimerFlags::empty(),
        &Itimerspec {
            it_interval: ts,
            it_value: ts,
        },
    )?;
    Ok(fd)
}

/// Drains the 8-byte expiration counter so the fd stops reporting readable.
pub fn drain_timerfd(fd: BorrowedFd) {
    let mut buf = [0u8; 8];
    let _ = read(fd, &mut buf);
}
