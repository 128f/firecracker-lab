use rustix::buffer::spare_capacity;
use rustix::event::epoll;
use rustix::io;
use std::os::fd::{BorrowedFd, OwnedFd};

use crate::boot::signals::SignalSource;

pub struct EpollGrid {
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
