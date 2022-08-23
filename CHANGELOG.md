# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.2.0] - 2022-08-23
### Added
- LICENSE.md
- CONTRIBUTING.md
- CI Dockerfile and GitHub workflows

### Changed
- Bump Lotus to v1.17.0
- Bump Boost to v1.3.1
- Filc blockstore is now stored with flatfs instead of lmdb
  - Old blockstores will no longer work, use [this migration
    tool](https://github.com/elijaharita/migrate-lmdb-flatfs) or simply clear
    the old blockstore `filc clear-blockstore`

### Fixed
- Protect libp2p connections while querying
- Filctl label string conversion

## [v0.1.0] - 2022-08-05
### Added
- First release