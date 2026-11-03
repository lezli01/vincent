package jiratest

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestBuildFakeJiraCompilesOnce(t *testing.T) {
	first := BuildFakeJira(t)
	if second := BuildFakeJira(t); second != first {
		t.Fatalf("two builds: %s and %s", first, second)
	}
}

// TestShutdownGivesUpOnAWedgedServer is review F12: a server that accepts
// the connection and never answers must not hold the cleanup, which only
// kills the process once Shutdown has returned.
func TestShutdownGivesUpOnAWedgedServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Hold every connection open and never reply; close them all on return.
	var mu sync.Mutex
	var conns []net.Conn
	defer func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	old := shutdownTimeout
	shutdownTimeout = 200 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = old })

	done := make(chan bool, 1)
	go func() { done <- (&Server{URL: "http://" + ln.Addr().String()}).Shutdown() }()
	select {
	case ok := <-done:
		if ok {
			t.Error("Shutdown reported success against a server that never answered")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown is still blocked on a server that never answers")
	}
}
