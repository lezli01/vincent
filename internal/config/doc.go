// Package config resolves platform-native config/data directories and
// loads, validates, and hot-reloads config.yaml (spec §12.2–12.3; T1.1).
//
// It imports exactly one internal package, internal/taskstate, to validate
// `notify.on` against §6's state vocabulary rather than keeping a second copy
// of it (task 046 decision 4). taskstate is itself a leaf — it imports only
// the standard library — so the dependency direction stays one-way.
package config
