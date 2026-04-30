use std::collections::BTreeMap;
use std::fs::File;
use std::io::Write;

use alloy_primitives::{B256, U256};
use alloy_trie::{nodes::BranchNodeCompact, TrieMask};
use reth_codecs::Compact;
use serde::Serialize;

#[derive(Serialize)]
struct Fixture {
    label: String,
    type_name: String,
    hex: String,
}

fn fix(label: &str, ty: &str, bytes: Vec<u8>) -> Fixture {
    Fixture {
        label: label.to_string(),
        type_name: ty.to_string(),
        hex: hex::encode(&bytes),
    }
}

fn enc<T: Compact>(v: &T) -> Vec<u8> {
    let mut buf = Vec::new();
    let _len = v.to_compact(&mut buf);
    buf
}

fn main() {
    let mut fixtures: Vec<Fixture> = Vec::new();

    // ---- u64 (zero-stripped) ----
    for v in [0u64, 1, 0xff, 0x100, 0xffff, 0x10000, 0xffffffff, u64::MAX] {
        fixtures.push(fix(&format!("u64_{:x}", v), "u64", enc(&v)));
    }

    // ---- U256 (zero-stripped) ----
    for v in [
        U256::from(0u64),
        U256::from(1u64),
        U256::from(0xffu64),
        U256::from(0x100u64),
        U256::MAX,
    ] {
        fixtures.push(fix(&format!("u256_{:x}", v), "U256", enc(&v)));
    }

    // ---- BranchNodeCompact ----
    let h1 = B256::repeat_byte(0x11);
    let h2 = B256::repeat_byte(0x22);

    let cases: Vec<(&str, BranchNodeCompact)> = vec![
        (
            "bnc_minimal",
            BranchNodeCompact::new(
                TrieMask::new(0u16),
                TrieMask::new(0u16),
                TrieMask::new(0u16),
                vec![],
                None,
            ),
        ),
        (
            "bnc_one_child",
            BranchNodeCompact::new(
                TrieMask::new(0x0001u16),
                TrieMask::new(0u16),
                TrieMask::new(0x0001u16),
                vec![h1],
                None,
            ),
        ),
        (
            "bnc_two_children_with_root",
            BranchNodeCompact::new(
                TrieMask::new(0x0003u16),
                TrieMask::new(0x0002u16),
                TrieMask::new(0x0003u16),
                vec![h1, h2],
                Some(h1),
            ),
        ),
    ];
    for (label, bnc) in cases {
        fixtures.push(fix(label, "BranchNodeCompact", enc(&bnc)));
    }

    // Group by type for readability.
    let mut grouped: BTreeMap<String, Vec<Fixture>> = BTreeMap::new();
    for f in fixtures {
        grouped.entry(f.type_name.clone()).or_default().push(f);
    }

    let json = serde_json::to_string_pretty(&grouped).unwrap();
    let mut f = File::create("../fixtures.json").expect("create fixtures.json");
    f.write_all(json.as_bytes()).expect("write fixtures.json");
    eprintln!("wrote {} fixture groups", grouped.len());
}
