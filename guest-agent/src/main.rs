use prost::Message;
use rustix::buffer::spare_capacity;
use rustix::event::epoll;
use rustix::fs::{Mode, mkdir};
use rustix::fs::{OFlags, fcntl_getfl, fcntl_setfl};
use rustix::io;
use rustix::io::Errno;
use rustix::io::read;
use rustix::mount::{MountFlags, mount};
use rustix::termios::{OptionalActions, tcgetattr, tcsetattr};
use std::io::{Read, Write, stdin};
use std::os::fd::{AsFd, BorrowedFd, FromRawFd, OwnedFd};
use std::sync::Arc;
use std::sync::atomic::{AtomicU8, AtomicU64, Ordering};
use syscalls::{Sysno, syscall};
use vsock::{VsockAddr, VsockListener, VsockStream};

mod status_api {
    include!(concat!(env!("OUT_DIR"), "/agent.rs"));
}
use status_api::{HealthStatus, StatusResponse, request::RequestType};

use crate::status_api::StatusRequest;

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

// VMADDR_CID_ANY = 0xFFFFFFFF (u32::MAX) — accept from any CID
const VMADDR_CID_ANY: u32 = u32::MAX;
const VSOCK_LISTEN_PORT: u32 = 1234;

/// SignalSource is the tag we apply to our signalfd signals
#[repr(u64)]
#[derive(Hash, PartialEq, Eq)]
enum SignalSource {
    Signal = 1,
    Stdin = 2,
    Pty = 3,
    VsockListen = 4,
}

impl SignalSource {
    fn from_u64(v: u64) -> Option<Self> {
        match v {
            1 => Some(SignalSource::Signal),
            2 => Some(SignalSource::Stdin),
            3 => Some(SignalSource::Pty),
            4 => Some(SignalSource::VsockListen),
            _ => None,
        }
    }
}

#[repr(u8)]
#[derive(Clone, Copy, PartialEq, Eq)]
enum Status {
    Init = 0,
    Running = 1,
    Stopped = 2,
}

struct Stats {
    status: AtomicU8,
    cpu_pct: AtomicU64, // or store as fixed-point / bits
    mem_bytes: AtomicU64,
    last_beat: AtomicU64,
}

impl Stats {
    fn new() -> Self {
        Stats {
            status: AtomicU8::new(Status::Init as u8),
            cpu_pct: AtomicU64::new(0),
            mem_bytes: AtomicU64::new(0),
            last_beat: AtomicU64::new(0),
        }
    }

    fn beat(&self) {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        self.last_beat.store(now, Ordering::Relaxed);
    }
}

fn mount_system_paths() {
    // TODO: this is sloppy af and needs a bunch of safeties
    // and likely a bunch more stuff needs to happen optionally
    let _ = mount("proc", "/proc", "proc", MountFlags::empty(), None);
    let _ = mount("sysfs", "/sys", "sysfs", MountFlags::empty(), None);
    let _ = mkdir("/dev/pts", Mode::from_raw_mode(0o755));
    let _ = mount("devpts", "/dev/pts", "devpts", MountFlags::empty(), None);
}

fn connect_signalfd() -> anyhow::Result<OwnedFd> {
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

struct VsockDispatcher {
    pub vsock_listener: VsockListener,
}

impl VsockDispatcher {
    fn new() -> std::io::Result<VsockDispatcher> {
        let listener = VsockListener::bind(&VsockAddr::new(VMADDR_CID_ANY, VSOCK_LISTEN_PORT))?;
        listener.set_nonblocking(true)?;
        Ok(VsockDispatcher {
            vsock_listener: listener,
        })
    }

    fn accept_connections(&mut self) -> Vec<(VsockStream, VsockAddr)> {
        let mut connections = vec![];
        loop {
            match self.vsock_listener.accept() {
                Ok((stream, addr)) => connections.push((stream, addr)),
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                    // we're done collecting connections
                    break;
                }
                Err(_e) => { /* TODO: do something */ }
            }
        }
        connections
    }
}

pub struct VsockConnectionHandler {
    stream: VsockStream,
    addr: VsockAddr,
}

impl VsockConnectionHandler {
    pub fn new(stream: VsockStream, addr: VsockAddr) -> VsockConnectionHandler {
        VsockConnectionHandler { stream, addr }
    }

    fn read_length_prefix(&mut self) -> std::io::Result<usize> {
        let mut buf = [0u8; 4];
        self.stream.read_exact(&mut buf)?;
        Ok(u32::from_be_bytes(buf) as usize)
    }

    fn extract_payload(&mut self, size: usize) -> anyhow::Result<status_api::Request> {
        let mut buf = vec![0u8; size];
        self.stream.read_exact(&mut buf)?;
        Ok(status_api::Request::decode(buf.as_slice())?)
    }

    fn send_response(&mut self, response: status_api::StatusResponse) -> anyhow::Result<()> {
        let buffer = response.encode_to_vec();
        let bytes = (buffer.len() as u32).to_be_bytes();
        self.stream.write_all(&bytes)?;
        self.stream.write_all(&buffer)?;
        Ok(())
    }
}

fn dispatch(req: status_api::Request, stats: &Stats) -> anyhow::Result<StatusResponse> {
    match req.request_type {
        Some(RequestType::Status(_status_req)) => handle_status(stats),
        None => anyhow::bail!("request with no request_type set"),
    }
}

fn handle_status(stats: &Stats) -> anyhow::Result<StatusResponse> {
    stats.beat();
    let status = match stats.status.load(Ordering::Relaxed) {
        s if s == Status::Running as u8 => HealthStatus::Healthy,
        _ => HealthStatus::Degraded,
    };
    Ok(StatusResponse {
        status: status as i32,
        cpu_pct: stats.cpu_pct.load(Ordering::Relaxed) as u32,
        mem_bytes: stats.mem_bytes.load(Ordering::Relaxed),
        last_beat: stats.last_beat.load(Ordering::Relaxed),
    })
}

fn setup_pty() -> pty_process::Result<(OwnedFd, OwnedFd)> {
    // TODO : s/unwrap/doinitrite/g
    let (pty, pts) = pty_process::blocking::open()?;
    let pty_fd: OwnedFd = rustix::io::dup(pty.as_fd())?;
    let pts_fd: OwnedFd = rustix::io::dup(pts.as_fd())?;
    pty_process::blocking::Command::new("/bin/sh")
        .spawn(pts)
        .unwrap();
    Ok((pty_fd, pts_fd))
}

fn drain_signals(signal_fd: BorrowedFd) {
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

fn forward_stdin(pty_fd: BorrowedFd) {
    let mut buf = [0u8; 4096];
    loop {
        match rustix::io::read(stdin(), &mut buf) {
            Ok(0) => break, // outside user EOF'd
            Ok(n) => {
                // n bytes to drain form stdin to master
                let mut off = 0;
                while off < n {
                    match rustix::io::write(pty_fd, &buf[off..n]) {
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

enum PtyStatus {
    Ok,
    HungUp,
}

fn forward_pty(pty_fd: BorrowedFd) -> PtyStatus {
    let mut stdout = std::io::stdout().lock();
    let mut buf = [0u8; 4096];
    loop {
        match rustix::io::read(pty_fd, &mut buf) {
            Ok(0) => return PtyStatus::HungUp,
            Ok(n) => {
                if stdout.write_all(&buf[..n]).is_err() {
                    // yeah I know it's not ok
                    // but probably this just means a temp write failure
                    // like the writer refuses
                    return PtyStatus::Ok;
                };
                let _ = stdout.flush();
            }
            Err(Errno::IO) => return PtyStatus::HungUp,
            Err(Errno::AGAIN) => return PtyStatus::Ok, // spurious wakeup / drained; keep looping
            Err(Errno::INTR) => continue,              // interrupted; retry next loop
            Err(_e) => return PtyStatus::Ok,           // TODO: actually handle the error tho
        };
    }
}

struct EpollGrid {
    epfd: OwnedFd,
    events: Vec<epoll::Event>,
}

impl EpollGrid {
    // Creates the base epollfd
    pub fn new() -> Self {
        let epfd = epoll::create(epoll::CreateFlags::CLOEXEC).unwrap();
        let events = Vec::with_capacity(8);
        EpollGrid { epfd, events }
    }

    pub fn add_epoll(&self, signal_tag: SignalSource, fd: BorrowedFd) -> io::Result<()> {
        epoll::add(
            &self.epfd,
            fd,
            epoll::EventData::new_u64(signal_tag as u64),
            epoll::EventFlags::IN,
        )?;
        Ok(())
    }

    pub fn get_events(&mut self) -> impl Iterator<Item = Option<SignalSource>> + '_ {
        // clear
        self.events.clear();
        // collect anew
        epoll::wait(&self.epfd, spare_capacity(&mut self.events), None).unwrap();
        // map back into tags
        self.events
            .iter()
            .map(|ev| SignalSource::from_u64(ev.data.u64()))
    }
}

struct RawTerminal {
    fd: BorrowedFd<'static>,
    orig: rustix::termios::Termios,
}
impl RawTerminal {
    fn make_raw(fd: BorrowedFd<'static>) -> anyhow::Result<Self> {
        let orig = tcgetattr(fd)?;
        let mut raw = orig.clone();
        raw.make_raw();
        tcsetattr(fd, OptionalActions::Flush, &raw)?;
        Ok(RawTerminal { fd, orig })
    }

    fn make_raw_stdin() -> anyhow::Result<Self> {
        let stdin = unsafe { BorrowedFd::borrow_raw(0) };
        RawTerminal::make_raw(stdin)
    }

    fn restore(&mut self) {
        // TODO: how about making this stick and then non-op?
        let _ = tcsetattr(self.fd, OptionalActions::Now, &self.orig);
    }
}
impl Drop for RawTerminal {
    fn drop(&mut self) {
        self.restore();
    }
}

fn main() {
    let mut raw_terminal = RawTerminal::make_raw_stdin().unwrap();
    mount_system_paths();
    let sfd = connect_signalfd().unwrap();
    let (pty_fd, _) = setup_pty().unwrap();

    let mut vsock_dispatcher = VsockDispatcher::new().unwrap();
    let stats = Arc::new(Stats::new());

    let mut epoller = EpollGrid::new();
    epoller
        .add_epoll(SignalSource::Signal, sfd.as_fd())
        .unwrap();
    epoller
        .add_epoll(SignalSource::Stdin, stdin().as_fd())
        .unwrap();
    epoller
        .add_epoll(SignalSource::Pty, pty_fd.as_fd())
        .unwrap();
    epoller
        .add_epoll(
            SignalSource::VsockListen,
            vsock_dispatcher.vsock_listener.as_fd(),
        )
        .unwrap();

    // set non-blocking for the pty connections
    fcntl_setfl(&pty_fd, fcntl_getfl(&pty_fd).unwrap() | OFlags::NONBLOCK).unwrap();
    fcntl_setfl(stdin(), fcntl_getfl(stdin()).unwrap() | OFlags::NONBLOCK).unwrap();

    let pty_fd = pty_fd.try_clone().unwrap();
    stats.status.store(Status::Running as u8, Ordering::Relaxed);
    loop {
        // consume
        for source in epoller.get_events() {
            match source {
                Some(SignalSource::Signal) => drain_signals(sfd.as_fd()),
                Some(SignalSource::Stdin) => forward_stdin(pty_fd.as_fd()),
                Some(SignalSource::Pty) => match forward_pty(pty_fd.as_fd()) {
                    PtyStatus::Ok => {}
                    PtyStatus::HungUp => raw_terminal.restore(),
                },
                Some(SignalSource::VsockListen) => {
                    for (stream, addr) in vsock_dispatcher.accept_connections() {
                        let stats = stats.clone();
                        std::thread::spawn(move || {
                            let mut handler = VsockConnectionHandler::new(stream, addr);
                            let Ok(prefix) = handler.read_length_prefix() else {
                                return;
                            };
                            let Ok(payload) = handler.extract_payload(prefix) else {
                                return;
                            };
                            let Ok(response) = dispatch(payload, &stats) else {
                                return;
                            };
                            let _ = handler.send_response(response);
                        });
                    }
                }
                None => {} // unknown tag; ignore
            }
        }
    }
}
