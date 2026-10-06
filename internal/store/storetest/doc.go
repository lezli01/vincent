// Package storetest opens test databases from a migrated template instead of
// migrating each one from nothing. It is imported only from _test files;
// nothing links it into the vincent binary.
//
// The cost it removes is the race detector's, not SQLite's. The store is
// modernc.org/sqlite, a pure-Go translation of the C library, so -race
// instruments every page SQLite touches: running all the embedded migrations
// on a new file takes ~12 ms without the detector and ~330 ms with it, and
// the suite opens a fresh database about 1,600 times — mostly one per test,
// mostly serial. Copying a file that was migrated once per test process and
// opening that takes ~8 ms under -race (issue #726).
//
// The template is made by store.Open itself, so the copy is exactly the
// database a real first open would leave behind, migrations' seed rows
// included. Tests of the migrations themselves keep calling store.Open on a
// new path: this package exists so the rest of the suite does not pay for
// them again.
package storetest
