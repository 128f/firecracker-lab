use rustix::fs::{Mode, mkdir};
use rustix::mount::{MountFlags, mount};

pub fn mount_system_paths() {
    // TODO: this is sloppy af and needs a bunch of safeties
    // and likely a bunch more stuff needs to happen optionally
    let _ = mount("proc", "/proc", "proc", MountFlags::empty(), None);
    let _ = mount("sysfs", "/sys", "sysfs", MountFlags::empty(), None);
    let _ = mkdir("/dev/pts", Mode::from_raw_mode(0o755));
    let _ = mount("devpts", "/dev/pts", "devpts", MountFlags::empty(), None);
}
