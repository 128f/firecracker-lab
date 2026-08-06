use std::sync::atomic::{AtomicU8, AtomicU64, Ordering};

use crate::proc_stats;

#[repr(u8)]
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Status {
    Init = 0,
    Running = 1,
    Stopped = 2,
}

pub struct Stats {
    pub status: AtomicU8,
    pub cpu_pct: AtomicU64,
    pub mem_available_bytes: AtomicU64,
    pub last_beat: AtomicU64,
    prev_cpu_total: AtomicU64,
    prev_cpu_idle: AtomicU64,
}

impl Stats {
    pub fn new() -> Self {
        Stats {
            status: AtomicU8::new(Status::Init as u8),
            cpu_pct: AtomicU64::new(0),
            mem_available_bytes: AtomicU64::new(0),
            last_beat: AtomicU64::new(0),
            prev_cpu_total: AtomicU64::new(0),
            prev_cpu_idle: AtomicU64::new(0),
        }
    }

    pub fn beat(&self) {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        self.last_beat.store(now, Ordering::Relaxed);
    }

    /// Takes a fresh /proc sample, reporting cpu_pct as the diff against the
    /// previous sample (a single /proc/stat read is meaningless - it's ticks
    /// since boot). Call this periodically from a background thread; last_beat
    /// reflects the most recent time this fired, i.e. proof the sampling loop
    /// is still alive.
    pub fn sample(&self) {
        self.beat();
        if let Ok((total, idle)) = proc_stats::read_cpu_ticks() {
            let prev_total = self.prev_cpu_total.swap(total, Ordering::Relaxed);
            let prev_idle = self.prev_cpu_idle.swap(idle, Ordering::Relaxed);
            let total_delta = total.saturating_sub(prev_total);
            let idle_delta = idle.saturating_sub(prev_idle);
            if total_delta > 0 {
                let pct = 100 * total_delta.saturating_sub(idle_delta) / total_delta;
                self.cpu_pct.store(pct, Ordering::Relaxed);
            }
        }
        if let Ok(available) = proc_stats::read_mem_available_bytes() {
            self.mem_available_bytes.store(available, Ordering::Relaxed);
        }
    }
}
