use rustix::buffer::spare_capacity;
use rustix::event::epoll;
use rustix::fs::{Mode, mkdir};
use rustix::fs::{OFlags, fcntl_getfl, fcntl_setfl};
use rustix::io::Errno;
use rustix::io::read;
use rustix::mount::{MountFlags, mount};
use std::io::{Write, stdin};
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

    // TODO : s/unwrap/doinitrite/g
    let (mut pty, mut pts) = pty_process::blocking::open().unwrap();
    let child = pty_process::blocking::Command::new("/bin/sh")
        .spawn(pts)
        .unwrap();

    let epfd = epoll::create(epoll::CreateFlags::CLOEXEC).unwrap();
    #[repr(u64)]
    enum Source {
        Signal = 1,
        Stdin = 2,
        Pty = 3,
    }

    impl Source {
        fn from_u64(v: u64) -> Option<Self> {
            match v {
                1 => Some(Source::Signal),
                2 => Some(Source::Stdin),
                3 => Some(Source::Pty),
                _ => None,
            }
        }
    }

    epoll::add(
        &epfd,
        sfd.as_fd(),
        epoll::EventData::new_u64(Source::Signal as u64),
        epoll::EventFlags::IN,
    )
    .unwrap();
    epoll::add(
        &epfd,
        &stdin(),
        epoll::EventData::new_u64(Source::Stdin as u64),
        epoll::EventFlags::IN,
    )
    .unwrap();
    epoll::add(
        &epfd,
        &pty,
        epoll::EventData::new_u64(Source::Pty as u64),
        epoll::EventFlags::IN,
    )
    .unwrap();

    let mut events: Vec<epoll::Event> = Vec::with_capacity(8);

    // TODO: println can panic prefer writeln and eat errors
    println!("spawned child with id {} ", child.id());

    // set non-blocking for the pty connections
    fcntl_setfl(&pty, fcntl_getfl(&pty).unwrap() | OFlags::NONBLOCK).unwrap();
    fcntl_setfl(&stdin(), fcntl_getfl(&stdin()).unwrap() | OFlags::NONBLOCK).unwrap();

    loop {
        let mut stdout = std::io::stdout().lock();
        // clear
        events.clear();
        // collect anew
        epoll::wait(&epfd, spare_capacity(&mut events), None).unwrap();
        // consume
        for ev in events.iter() {
            match Source::from_u64(ev.data.u64()) {
                Some(Source::Signal) => {
                    // drain signalfd
                    let mut buf = [0u8; 128 * 4];
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
                Some(Source::Stdin) => {
                    let mut buf = [0u8; 4096];
                    loop {
                        match rustix::io::read(stdin(), &mut buf) {
                            Ok(0) => break, // outside user EOF'd
                            Ok(n) => {
                                // n bytes to drain form stdin to master
                                let mut off = 0;
                                while off < n {
                                    match rustix::io::write(&pty, &buf[off..n]) {
                                        Ok(w) => off += w,
                                        Err(Errno::INTR) => continue,
                                        // TODO: can we lock up here?
                                        Err(Errno::AGAIN) => continue,
                                        Err(_e) => break, // master can't be written
                                    }
                                }
                            }
                            Err(Errno::AGAIN) => break, // stdin drained
                            Err(Errno::INTR) => continue,
                            Err(_e) => break,
                        }
                    }
                }
                Some(Source::Pty) => {
                    let mut buf = [0u8; 4096];
                    loop {
                        match rustix::io::read(&pty, &mut buf) {
                            Ok(0) => { /* TODO: hungup, respawn shell and re-enter */ }
                            Ok(n) => {
                                if stdout.write_all(&buf[..n]).is_err() {
                                    break;
                                };
                                let _ = stdout.flush();
                            }
                            Err(Errno::AGAIN) => break, // spurious wakeup / drained; keep looping
                            Err(Errno::INTR) => continue, // interrupted; retry next loop
                            Err(_e) => break,           // TODO: handle the error
                        };
                    }
                }
                None => {} // unknown tag; ignore
            }
        }
    }
}
