// Package api serves the localhost HTTP API (spec §13): bearer-auth
// middleware with a health exemption, request logging, panic recovery, and
// the JSON error envelope with stable snake_case codes. Domain endpoints
// (projects, tasks) and SSE streams land with T1.5+ and T2.7.
package api
