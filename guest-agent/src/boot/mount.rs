use rustix::fs::{Mode, mkdir};
use rustix::io::Errno;
use rustix::mount::{MountFlags, mount};

pub struct MountSpec {
    pub source: &'static str,
    pub target: &'static str,
    pub fstype: &'static str,
    /// Create `target` as a directory before mounting, for paths that may not exist yet.
    pub create_target_dir: bool,
}

pub struct MountConfig {
    pub specs: Vec<MountSpec>,
}

impl Default for MountConfig {
    fn default() -> Self {
        MountConfig {
            specs: vec![
                MountSpec {
                    source: "proc",
                    target: "/proc",
                    fstype: "proc",
                    create_target_dir: false,
                },
                MountSpec {
                    source: "sysfs",
                    target: "/sys",
                    fstype: "sysfs",
                    create_target_dir: false,
                },
                MountSpec {
                    source: "devpts",
                    target: "/dev/pts",
                    fstype: "devpts",
                    create_target_dir: true,
                },
            ],
        }
    }
}

/// Mounts the given paths. Safe to call more than once (e.g. after a
/// snapshot restore): each step tolerates "already mounted"/"already
/// exists" and only fails on a real error.
pub fn mount_system_paths(config: MountConfig) -> anyhow::Result<()> {
    for spec in &config.specs {
        if spec.create_target_dir {
            make_dir(spec.target)?;
        }
        mount_fs(spec.source, spec.target, spec.fstype)?;
    }
    Ok(())
}

fn mount_fs(source: &str, target: &str, fstype: &str) -> anyhow::Result<()> {
    match mount(source, target, fstype, MountFlags::empty(), None) {
        Ok(()) => Ok(()),
        Err(Errno::BUSY) => {
            eprintln!("{target} already mounted, skipping");
            Ok(())
        }
        Err(e) => Err(anyhow::anyhow!("failed to mount {fstype} at {target}: {e}")),
    }
}

fn make_dir(path: &str) -> anyhow::Result<()> {
    match mkdir(path, Mode::from_raw_mode(0o755)) {
        Ok(()) => Ok(()),
        Err(Errno::EXIST) => Ok(()),
        Err(e) => Err(anyhow::anyhow!("failed to create {path}: {e}")),
    }
}
