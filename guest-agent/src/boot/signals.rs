use rustix::io::read;
use std::os::fd::{BorrowedFd, FromRawFd, OwnedFd};
use syscalls::{Sysno, syscall};

// kernel sigset_t size on x86-64 = _NSIG/8 = 8 bytes (NOT userspace sizeof)
const SIGSET_SIZE: usize = 8;

// signalfd4 flags — SFD_CLOEXEC | SFD_NONBLOCK (== O_CLOEXEC | O_NONBLOCK)
const SFD_CLOEXEC: usize = 0o2000000;
const SFD_NONBLOCK: usize = 0o0004000;

// rt_sigprocmask how
const SIG_BLOCK: usize = 0;

// signal codes
const SIGCHLD: u64 = 17;
const SIGTERM: u64 = 15;

const WAIT_ANY: usize = usize::MAX; // pid = -1: reap any child
const WNOHANG: usize = 1; // return immediately if none ready

/// SignalSource is the tag we apply to our signalfd signals
#[repr(u64)]
#[derive(Hash, PartialEq, Eq)]
pub enum SignalSource {
    Signal = 1,
    Stdin = 2,
    Pty = 3,
    VsockListen = 4,
    Timer = 5,
}

impl SignalSource {
    pub fn from_u64(v: u64) -> Option<Self> {
        match v {
            1 => Some(SignalSource::Signal),
            2 => Some(SignalSource::Stdin),
            3 => Some(SignalSource::Pty),
            4 => Some(SignalSource::VsockListen),
            5 => Some(SignalSource::Timer),
            _ => None,
        }
    }
}

pub fn connect_signalfd() -> anyhow::Result<OwnedFd> {
    // block SIGCHLD+SIGTERM, then signalfd them
    let mask: u64 = (1 << (SIGCHLD - 1)) | (1 << (SIGTERM - 1));

    unsafe {
        syscall!(
            Sysno::rt_sigprocmask,
            SIG_BLOCK,                    // "how" - block, unblock, or mask
            &mask as *const u64 as usize, //set
            0usize,                       // oldset, unused
            SIGSET_SIZE
        )?;
    }
    let raw = unsafe {
        syscall!(
            Sysno::signalfd4,
            usize::MAX,
            &mask as *const u64 as usize,
            SIGSET_SIZE,
            SFD_CLOEXEC | SFD_NONBLOCK
        )?
    };
    unsafe { Ok(OwnedFd::from_raw_fd(raw as i32)) }
}

pub fn drain_signals(signal_fd: BorrowedFd) {
    // drain signalfd
    let mut buf = [0u8; 128 * 4];
    while let Ok(n) = read(signal_fd, &mut buf) {
        if n == 0 {
            break;
        }
    }
    // reap
    loop {
        match unsafe {
            syscall!(
                Sysno::wait4,
                WAIT_ANY, // pid to listen for
                0usize,   // don't capture exit status
                WNOHANG,  // options
                0usize    // don't capture resource usage
            )
        } {
            Ok(0) | Err(_) => break,
            Ok(_) => continue,
        }
    }
}
