use std::net::SocketAddr;
use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct Client {
    #[serde(rename = "ClientGuid")]
    pub id: Option<String>,
    #[serde(skip)]
    pub addr: Option<SocketAddr>,
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Coalition")]
    pub coalition: i32,
    #[serde(rename = "AllowRecord")]
    pub allow_record: bool,
    #[serde(rename = "RadioInfo")]
    pub radio_information: Option<RadioInformation>,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct RadioInformation {
    pub radios: Vec<Radio>,
    pub unit: String,
    #[serde(rename = "unitId")]
    pub unit_id: i32,
    pub iff: Iff,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct Radio {
    pub enc: bool,
    #[serde(rename = "encKey")]
    pub enc_key: i32,
    pub freq: f64,
    #[serde(rename = "standbyfreq")]
    pub standby_freq: f64,
    pub modulation: i32,
    #[serde(rename = "type")]
    pub radio_type: i32,
    #[serde(rename = "secFreq")]
    pub sec_freq: f64,
    pub retransmit: bool,
    #[serde(rename = "standbychannel")]
    pub standby_channel: i32,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct Iff {
    pub control: i32,
    pub mode1: i32,
    pub mode3: i32,
    pub mode4: bool,
    pub mic: i32,
    pub status: i32,
}

impl Client {
    pub fn new(id: String, addr: SocketAddr) -> Self {
        Self { id: Some(id), addr: Some(addr), allow_record: false, coalition: 0, name: "".to_string(), radio_information: None }
    }
}
