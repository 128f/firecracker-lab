use std::fs;

/// Returns (total_ticks, idle_ticks) summed from the aggregate "cpu" line of /proc/stat.
pub fn read_cpu_ticks() -> anyhow::Result<(u64, u64)> {
    let contents = fs::read_to_string("/proc/stat")?;
    let line = contents
        .lines()
        .next()
        .ok_or_else(|| anyhow::anyhow!("/proc/stat is empty"))?;
    let fields: Vec<u64> = line
        .split_whitespace()
        .skip(1) // "cpu" label
        .filter_map(|f| f.parse().ok())
        .collect();
    if fields.len() < 4 {
        anyhow::bail!("unexpected /proc/stat cpu line: {line}");
    }
    let total: u64 = fields.iter().sum();
    let idle = fields[3] + fields.get(4).copied().unwrap_or(0); // idle + iowait
    Ok((total, idle))
}

/// Returns MemAvailable from /proc/meminfo, in bytes.
pub fn read_mem_available_bytes() -> anyhow::Result<u64> {
    let contents = fs::read_to_string("/proc/meminfo")?;
    for line in contents.lines() {
        if let Some(rest) = line.strip_prefix("MemAvailable:") {
            let kb: u64 = rest.trim().trim_end_matches(" kB").trim().parse()?;
            return Ok(kb * 1024);
        }
    }
    anyhow::bail!("MemAvailable not found in /proc/meminfo")
}
