use rustix::buffer::spare_capacity;
use rustix::event::epoll;
use rustix::io;
use std::os::fd::{AsRawFd, BorrowedFd, OwnedFd, RawFd};

use crate::boot::signals::SignalSource;

pub struct PollSpec<'fd> {
    pub tag: SignalSource,
    pub fd: BorrowedFd<'fd>,
}

#[derive(Default)]
pub struct PollConfig<'fd> {
    pub specs: Vec<PollSpec<'fd>>,
}

impl<'fd> PollConfig<'fd> {
    pub fn with(mut self, tag: SignalSource, fd: BorrowedFd<'fd>) -> Self {
        self.specs.push(PollSpec { tag, fd });
        self
    }
}

pub struct Poller {
    epfd: OwnedFd,
    events: Vec<epoll::Event>,
    registered: Vec<(SignalSource, RawFd)>,
}

impl Poller {
    // Creates the base epollfd
    pub fn new() -> Self {
        let epfd = epoll::create(epoll::CreateFlags::CLOEXEC).unwrap();
        let events = Vec::with_capacity(8);
        Poller {
            epfd,
            events,
            registered: Vec::new(),
        }
    }

    /// Registers `fd` under `signal_tag`. Safe to call more than once (e.g.
    /// after a snapshot restore re-runs boot): if `fd` is already
    /// registered, its tag/flags are updated in place instead of erroring.
    pub fn add_epoll(&mut self, signal_tag: SignalSource, fd: BorrowedFd) -> io::Result<()> {
        let data = epoll::EventData::new_u64(signal_tag as u64);
        match epoll::add(&self.epfd, fd, data, epoll::EventFlags::IN) {
            Ok(()) => {}
            Err(io::Errno::EXIST) => epoll::modify(&self.epfd, fd, data, epoll::EventFlags::IN)?,
            Err(e) => return Err(e),
        }
        let raw = fd.as_raw_fd();
        match self.registered.iter_mut().find(|(_, r)| *r == raw) {
            Some(entry) => entry.0 = signal_tag,
            None => self.registered.push((signal_tag, raw)),
        }
        Ok(())
    }

    /// Registers every spec in `config` in one call.
    pub fn add_all(&mut self, config: PollConfig) -> io::Result<()> {
        for spec in config.specs {
            self.add_epoll(spec.tag, spec.fd)?;
        }
        Ok(())
    }

    /// Unregisters `fd`. No-op if it wasn't registered.
    pub fn remove_epoll(&mut self, fd: BorrowedFd) -> io::Result<()> {
        match epoll::delete(&self.epfd, fd) {
            Ok(()) | Err(io::Errno::NOENT) => {}
            Err(e) => return Err(e),
        }
        let raw = fd.as_raw_fd();
        self.registered.retain(|(_, r)| *r != raw);
        Ok(())
    }

    /// Iterates over the currently registered (tag, fd) pairs.
    pub fn registrations(&self) -> impl Iterator<Item = (SignalSource, RawFd)> + '_ {
        self.registered.iter().copied()
    }

    /// Removes every current registration, e.g. before re-running boot on a
    /// snapshot restore. Individual removal failures are ignored since a
    /// stale/closed fd is already effectively gone from epoll's perspective.
    pub fn purge_all(&mut self) {
        for (_, raw) in self.registered.drain(..) {
            let fd = unsafe { BorrowedFd::borrow_raw(raw) };
            let _ = epoll::delete(&self.epfd, fd);
        }
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
