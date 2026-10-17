package web

import (
	"net"
	"testing"
	"time"
)

func TestAuthLimiter(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	l := newAuthLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		l.fail("1.2.3.4")
	}
	if l.blockedFor("1.2.3.4") != 0 {
		t.Fatal("blocked too early")
	}
	l.fail("1.2.3.4")
	if d := l.blockedFor("1.2.3.4"); d != time.Minute {
		t.Fatalf("blocked for %v", d)
	}
	if l.blockedFor("5.6.7.8") != 0 {
		t.Fatal("another address must not be affected")
	}
	now = now.Add(61 * time.Second)
	if l.blockedFor("1.2.3.4") != 0 {
		t.Fatal("block must expire")
	}

	// failures spread over more than the window do not add up
	l2 := newAuthLimiter()
	l2.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		l2.fail("9.9.9.9")
	}
	now = now.Add(2 * time.Minute)
	for i := 0; i < 4; i++ {
		l2.fail("9.9.9.9")
	}
	if l2.blockedFor("9.9.9.9") != 0 {
		t.Fatal("old failures must be forgotten")
	}

	// a correct login clears the count
	l3 := newAuthLimiter()
	for i := 0; i < 4; i++ {
		l3.fail("a")
	}
	l3.ok("a")
	l3.fail("a")
	if l3.blockedFor("a") != 0 {
		t.Fatal("ok() must reset")
	}

	// the table cannot grow without bound
	l4 := newAuthLimiter()
	for i := 0; i < maxLimiterEntries*3; i++ {
		l4.fail(string(rune('a'+i%26)) + time.Duration(i).String())
	}
	if len(l4.entries) > maxLimiterEntries {
		t.Fatalf("%d entries", len(l4.entries))
	}
}

func TestLimitListener(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(base, 2)
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", base.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var cs []net.Conn
	for i := 0; i < 3; i++ {
		cs = append(cs, dial())
	}
	defer func() {
		for _, c := range cs {
			c.Close()
		}
	}()
	s1, s2 := <-accepted, <-accepted
	select {
	case <-accepted:
		t.Fatal("a third connection was accepted while two are open")
	case <-time.After(300 * time.Millisecond):
	}
	s1.Close()
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("a slot was not released after Close")
	}
	s2.Close()
	s1.Close() // closing twice must not release twice
}
