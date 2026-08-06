use std::sync::atomic::{AtomicU8, AtomicU64, Ordering};

#[repr(u8)]
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Status {
    Init = 0,
    Running = 1,
    Stopped = 2,
}

pub struct Stats {
    pub status: AtomicU8,
    pub cpu_pct: AtomicU64, // or store as fixed-point / bits
    pub mem_bytes: AtomicU64,
    pub last_beat: AtomicU64,
}

impl Stats {
    pub fn new() -> Self {
        Stats {
            status: AtomicU8::new(Status::Init as u8),
            cpu_pct: AtomicU64::new(0),
            mem_bytes: AtomicU64::new(0),
            last_beat: AtomicU64::new(0),
        }
    }

    pub fn beat(&self) {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        self.last_beat.store(now, Ordering::Relaxed);
    }
}
