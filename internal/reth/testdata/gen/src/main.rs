use std::collections::BTreeMap;
use std::fs::File;
use std::io::Write;

use alloy_primitives::{B256, U256};
use alloy_trie::{HashBuilder, Nibbles, nodes::BranchNodeCompact, TrieMask};
use reth_codecs::Compact;
use roaring::RoaringTreemap;
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

    // ---- IntegerList (RoaringTreemap serialization) ----
    // Mirrors reth-db-api's IntegerList::to_bytes() which calls
    // RoaringTreemap::serialize_into (crates/storage/db-api/src/models/integer_list.rs).
    let il_cases: Vec<(&str, Vec<u64>)> = vec![
        ("il_empty", vec![]),
        ("il_single", vec![0]),
        ("il_small", vec![0, 1, 2, 3]),
        ("il_sparse", vec![0, 100, 200, 0x12345678]),
    ];
    for (label, values) in il_cases {
        let bm = RoaringTreemap::from_sorted_iter(values.into_iter()).expect("sorted");
        let mut bytes = Vec::with_capacity(bm.serialized_size());
        bm.serialize_into(&mut bytes).expect("serialize");
        fixtures.push(fix(label, "IntegerList", bytes));
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

    // ---- StorageTrieEntry (PackedStorageTrieEntry, v2 33-byte subkey) ----
    //
    // reth-trie-common is not published on crates.io, so we construct the bytes
    // manually using the same layout as PackedStoredNibblesSubKey::to_compact +
    // BranchNodeCompact::to_compact (storage.rs:71-86).
    //
    // PackedStoredNibblesSubKey wire (33 bytes):
    //   bytes [0..31]: nibbles packed 2-per-byte (high nibble first), zero-padded right
    //   byte  [32]:    nibble count (0..=64)
    //
    // Wire: SubKey(33B) || BranchNodeCompact(...)
    {
        // ste_minimal: empty SubKey (length 0, packed = 0x00*32) + minimal BNC
        // SubKey: [0x00 * 32] ++ [0x00]  (33 bytes, length=0)
        // BNC: 000000000000 (6 bytes, all-zero masks, no hashes)
        let mut ste_minimal = vec![0u8; 33]; // all zeros = packed zeros + length=0
        let bnc_minimal = enc(&BranchNodeCompact::new(
            TrieMask::new(0), TrieMask::new(0), TrieMask::new(0), vec![], None,
        ));
        ste_minimal.extend_from_slice(&bnc_minimal);
        fixtures.push(fix("ste_minimal", "StorageTrieEntry", ste_minimal));

        // ste_basic: SubKey length=4, nibbles=[1,2,3,4] + 1-child BNC (hash=0xaa*32)
        // packed: nibble 1 in high, 2 in low of byte 0 → 0x12; nibble 3 hi, 4 lo of byte 1 → 0x34
        // bytes [0..1] = [0x12, 0x34], bytes [2..31] = 0x00, byte [32] = 0x04
        let h_aa = B256::repeat_byte(0xaa);
        let mut subkey_basic = [0u8; 33];
        subkey_basic[0] = 0x12; // nibbles 1,2
        subkey_basic[1] = 0x34; // nibbles 3,4
        subkey_basic[32] = 4;   // length
        let bnc_basic = enc(&BranchNodeCompact::new(
            TrieMask::new(0x0001), TrieMask::new(0x0000), TrieMask::new(0x0001),
            vec![h_aa], None,
        ));
        let mut ste_basic = subkey_basic.to_vec();
        ste_basic.extend_from_slice(&bnc_basic);
        fixtures.push(fix("ste_basic", "StorageTrieEntry", ste_basic));

        // ste_with_root: SubKey length=8, nibbles=[1,2,3,4,5,6,7,8]
        // packed: [0x12, 0x34, 0x56, 0x78, 0x00*28], length=8
        // BNC: StateMask=0x0003, TreeMask=0x0002, HashMask=0x0003, hashes=[0xaa*32, 0xbb*32], root=0xbb*32
        let h_bb = B256::repeat_byte(0xbb);
        let mut subkey_root = [0u8; 33];
        subkey_root[0] = 0x12;
        subkey_root[1] = 0x34;
        subkey_root[2] = 0x56;
        subkey_root[3] = 0x78;
        subkey_root[32] = 8;
        let bnc_root = enc(&BranchNodeCompact::new(
            TrieMask::new(0x0003), TrieMask::new(0x0002), TrieMask::new(0x0003),
            vec![h_aa, h_bb], Some(h_bb),
        ));
        let mut ste_root = subkey_root.to_vec();
        ste_root.extend_from_slice(&bnc_root);
        fixtures.push(fix("ste_with_root", "StorageTrieEntry", ste_root));
    }

    // ---- HashBuilder (alloy_trie::HashBuilder) ----
    //
    // For each case we emit two fixtures:
    //   HashBuilderRoot:      label hb_<case>_root,      32-byte canonical root
    //   HashBuilderEmissions: label hb_<case>_emissions, concat of emission frames
    //
    // Emission frame layout per (path, BranchNodeCompact) entry from
    // HashBuilder::split() updates (path-sorted, BTreeMap order = lexicographic on
    // nibble paths):
    //   path[33]:  packed[32] || length[1]   (StoredNibbles 33-byte wire form)
    //   bnc_len[2]: big-endian u16 length of compact-encoded BNC
    //   bnc_bytes[bnc_len]: BranchNodeCompact::to_compact output

    // Helper: encode Nibbles path as 33-byte StoredNibbles wire (packed[32] || length[1]).
    fn nibbles_to_stored(nibbles: &Nibbles) -> [u8; 33] {
        let mut out = [0u8; 33];
        nibbles.pack_to(&mut out[..32]);
        out[32] = nibbles.len() as u8;
        out
    }

    // Helper: run HashBuilder over sorted leaves, return (root_bytes, emissions_bytes).
    fn run_hash_builder(leaves: Vec<(Vec<u8>, Vec<u8>)>) -> (Vec<u8>, Vec<u8>) {
        let mut hb = HashBuilder::default().with_updates(true);
        for (key_bytes, value) in &leaves {
            let nibbles = Nibbles::unpack(key_bytes.as_slice());
            hb.add_leaf(nibbles, value.as_slice());
        }
        let root = hb.root();
        let (_hb2, updates) = hb.split();

        // Collect emissions in sorted nibble-path order (BTreeMap is already sorted).
        let mut emissions_buf = Vec::new();
        for (path_nibbles, bnc) in &updates {
            // Path: 33-byte StoredNibbles wire
            let path_wire = nibbles_to_stored(path_nibbles);
            emissions_buf.extend_from_slice(&path_wire);
            // BNC compact encoding
            let bnc_bytes = enc(bnc);
            let bnc_len = bnc_bytes.len() as u16;
            emissions_buf.push((bnc_len >> 8) as u8);
            emissions_buf.push(bnc_len as u8);
            emissions_buf.extend_from_slice(&bnc_bytes);
        }

        (root.as_slice().to_vec(), emissions_buf)
    }

    // Case definitions. Keys are raw 32-byte byte arrays (not yet nibble-unpacked).
    // Leaves must be in sorted order (sorted on the raw key bytes which become nibble
    // paths after Nibbles::unpack, which preserves byte order).
    let hb_cases: Vec<(&str, Vec<(Vec<u8>, Vec<u8>)>)> = vec![
        // hb_single: single 32-byte key [0xa0; 32], value [0x42; 32]
        (
            "hb_single",
            vec![
                (vec![0xa0u8; 32], vec![0x42u8; 32]),
            ],
        ),
        // hb_two_shared: two leaves sharing 31-byte prefix, differing at byte 31
        (
            "hb_two_shared",
            {
                let k1 = vec![0xa0u8; 32];
                let mut k2 = vec![0xa0u8; 32];
                k2[31] = 0xa1;
                vec![
                    (k1, vec![0x01u8]),
                    (k2, vec![0x02u8]),
                ]
            },
        ),
        // hb_three_top: three leaves diverging at byte 0 (no shared prefix)
        (
            "hb_three_top",
            {
                let mut k1 = vec![0u8; 32];
                let mut k2 = vec![0u8; 32];
                let mut k3 = vec![0u8; 32];
                k1[0] = 0x10;
                k2[0] = 0x20;
                k3[0] = 0x30;
                vec![
                    (k1, vec![0x01u8]),
                    (k2, vec![0x02u8]),
                    (k3, vec![0x03u8]),
                ]
            },
        ),
        // hb_full_branch: 16 leaves at byte 0 with each high-nibble (0x00, 0x10, ..., 0xf0)
        (
            "hb_full_branch",
            {
                (0u8..16u8).map(|i| {
                    let mut k = vec![0u8; 32];
                    k[0] = i << 4;
                    (k, vec![i + 1])
                }).collect()
            },
        ),
    ];

    for (case_name, leaves) in hb_cases {
        let (root_bytes, emissions_bytes) = run_hash_builder(leaves);
        fixtures.push(fix(
            &format!("{}_root", case_name),
            "HashBuilderRoot",
            root_bytes,
        ));
        fixtures.push(fix(
            &format!("{}_emissions", case_name),
            "HashBuilderEmissions",
            emissions_bytes,
        ));
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
