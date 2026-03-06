# NEXORA Core

Rust + Axum runtime exposing NEXORA API v24.8.

## Build

```powershell
cargo build --manifest-path core/Cargo.toml
```

## Run

```powershell
$env:NEXORA_ROOT = (Get-Location).Path
cargo run --manifest-path core/Cargo.toml
```

## Default local URL

- http://127.0.0.1:8080
