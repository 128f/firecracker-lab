use rustix::buffer::spare_capacity;
use rustix::event::epoll;
use rustix::fs::{Mode, mkdir};
use rustix::io::read;
use rustix::mount::{MountFlags, mount};
use std::os::fd::{AsFd, FromRawFd, OwnedFd};
use syscalls::{Sysno, syscall};

fn early_mounts() {
    // TODO: this is sloppy af and needs a bunch of safeties
    // and likely a bunch more stuff needs to happen optionally
    let _ = mount("proc", "/proc", "proc", MountFlags::empty(), None);
    let _ = mount("sysfs", "/sys", "sysfs", MountFlags::empty(), None);
    let _ = mkdir("/dev/pts", Mode::from_raw_mode(0o755));
    let _ = mount("devpts", "/dev/pts", "devpts", MountFlags::empty(), None);
}

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

fn main() {
    early_mounts();
    // block SIGCHLD+SIGTERM, then signalfd them
    let mask: u64 = (1 << (SIGCHLD - 1)) | (1 << (SIGTERM - 1));

    unsafe {
        syscall!(
            Sysno::rt_sigprocmask,
            SIG_BLOCK,                    // "how" - block, unblock, or mask
            &mask as *const u64 as usize, //set
            0usize,                       // oldset, unused
            SIGSET_SIZE
        )
        .unwrap();
    }
    let raw = unsafe {
        syscall!(
            Sysno::signalfd4,
            usize::MAX,
            &mask as *const u64 as usize,
            SIGSET_SIZE,
            SFD_CLOEXEC | SFD_NONBLOCK
        )
        .unwrap()
    };
    let sfd = unsafe { OwnedFd::from_raw_fd(raw as i32) };

    let epfd = epoll::create(epoll::CreateFlags::CLOEXEC).unwrap();
    epoll::add(
        &epfd,
        sfd.as_fd(),
        epoll::EventData::new_u64(1),
        epoll::EventFlags::IN,
    )
    .unwrap();

    let mut events: Vec<epoll::Event> = Vec::with_capacity(8);
    let mut buf = [0u8; 128 * 4];

    loop {
        //clear
        events.clear();
        //collect
        epoll::wait(&epfd, spare_capacity(&mut events), None).unwrap();
        //consume
        for ev in events.iter() {
            if ev.data.u64() == 1 {
                // drain signalfd
                while let Ok(n) = read(sfd.as_fd(), &mut buf) {
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
        }
    }
}
