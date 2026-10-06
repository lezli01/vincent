// Package wait is the test suite's one poll-until-true helper and the one
// place a test budget is scaled (#731).
//
// Every wait in the suite is a fixed wall-clock budget sized for a quiet
// machine, and CI's Windows leg is not one: the budgets were
// overrun there one package at a time, each with its own copy of the same
// loop and its own number. Timeout multiplies a budget by the factor in
// VINCENT_TEST_TIMEOUT_SCALE (1 when unset), which ci.yml sets on that leg
// only, so a slow runner gets more time without a quiet one waiting longer
// for a test that really fails.
//
// The same factor reaches the budgets tests inherit from production, which
// cannot import this package: apiclient.New scales its REST timeout and
// `vincent daemon start` its health poll by it, through
// apiclient.ScaleTimeout. Production never sets the variable, so its
// defaults are unchanged.
//
// It is test support: only _test.go files import it.
package wait
