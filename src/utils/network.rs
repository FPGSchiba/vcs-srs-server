use log::trace;
use sha2::{Digest, Sha256};

pub fn is_version_compatible(version: &str) -> bool {
    version == "2.0.8.4" // Current DCS SRS Version, that was modified
}

pub fn get_sha256_hash(data: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(data.as_bytes());
    let result = hasher.finalize();
    let str_result = format!("{:x}", result);
    trace!("Hashed {} to {}", data, str_result);
    str_result
}
