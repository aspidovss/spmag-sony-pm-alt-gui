package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
)

// Access describes how the web interface is offered: it is what the
// "Web interface" settings (port, login, password, network access) mean.
type Access struct {
	Enabled  bool
	Port     int
	User     string
	Password string
	Remote   bool // reachable from other computers; otherwise only from this one
}

// AccessFrom reads the web options out of the settings.
func AccessFrom(s config.Settings) Access {
	return Access{Enabled: s.WebEnabled, Port: s.WebPort, User: s.WebUser, Password: s.WebPassword, Remote: s.WebRemote}
}

// Apply copies the web options into the settings.
func (a Access) Apply(s *config.Settings) {
	s.WebEnabled, s.WebPort, s.WebUser, s.WebPassword, s.WebRemote = a.Enabled, a.Port, a.User, a.Password, a.Remote
}

// Normalize fills in the default port and login.
func (a Access) Normalize() Access {
	if a.Port == 0 {
		a.Port = config.DefaultWebPort
	}
	a.User = strings.TrimSpace(a.User)
	if a.User == "" {
		a.User = config.DefaultWebUser
	}
	return a
}

// Validate returns a translated, user-presentable error for impossible
// combinations. A disabled interface is always valid.
func (a Access) Validate() error {
	if !a.Enabled {
		return nil
	}
	if a.Port < 1 || a.Port > 65535 {
		return errors.New(i18n.T("dlg.webPort"))
	}
	if a.Remote && a.Password == "" {
		return errors.New(i18n.T("dlg.webNeedPass"))
	}
	return nil
}

// Listen turns the access options into a listen address.
func (a Access) Listen() Listen {
	host := "127.0.0.1"
	if a.Remote {
		host = "" // all interfaces
	}
	return Listen{Addr: net.JoinHostPort(host, strconv.Itoa(a.Port)), User: a.User, Password: a.Password}
}

// Listen is a concrete address plus credentials.
type Listen struct {
	Addr     string
	User     string // empty = any user name
	Password string // empty = no authentication (loopback only unless the caller allows more)
}

// Status is what the settings screens show about the running server.
type Status struct {
	Running bool
	Addr    string // as the user would type it into a browser, e.g. 192.168.1.5:8080
	Err     string
}

// Manager starts, stops and restarts the web server while the program runs, so
// that the port, login and password can be changed without restarting it.
type Manager struct {
	base Options

	mu     sync.Mutex
	srv    *http.Server
	done   chan struct{}
	status Status
	pinned bool

	// OnSettings is called (from a server goroutine) after settings were saved
	// through the web page, so that a desktop window can refresh its form.
	OnSettings func(config.Settings)
}

// NewManager prepares a manager. base supplies the controller, log buffer and
// version; the credentials come from Start.
func NewManager(base Options) *Manager {
	base.Manager = nil
	return &Manager{base: base}
}

// Pin marks the listener as configured from outside (command line or
// environment): the web page then shows its web options as read-only.
func (m *Manager) Pin() {
	m.mu.Lock()
	m.pinned = true
	m.mu.Unlock()
}

// Pinned reports whether the web options are fixed by the command line.
func (m *Manager) Pinned() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pinned
}

// Status returns the current state of the server.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Apply starts, restarts or stops the server to match a. Errors (port in use…)
// are returned and also kept in Status.
func (m *Manager) Apply(a Access) error {
	a = a.Normalize()
	if !a.Enabled {
		m.Stop()
		return nil
	}
	if err := a.Validate(); err != nil {
		m.setErr(err)
		return err
	}
	return m.Start(a.Listen(), false)
}

// Start (re)starts the server on l. allowOpen permits a non-loopback address
// without a password (the caller has been told so explicitly).
func (m *Manager) Start(l Listen, allowOpen bool) error {
	loop := IsLoopbackAddr(l.Addr)
	if !loop && l.Password == "" && !allowOpen {
		err := errors.New(i18n.T("dlg.webNeedPass"))
		m.setErr(err)
		return err
	}
	m.Stop()

	ln, err := net.Listen("tcp", l.Addr)
	if err != nil {
		m.setErr(err)
		return err
	}
	o := m.base
	o.User, o.Password = l.User, l.Password
	o.LoopbackOnly = loop && l.Password == ""
	o.Manager = m
	srv := &http.Server{
		Handler:           Handler(o),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	ln = newLimitListener(ln, 128)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	m.mu.Lock()
	m.srv, m.done = srv, done
	m.status = Status{Running: true, Addr: DisplayAddr(ln.Addr().String())}
	m.mu.Unlock()
	return nil
}

// Stop shuts the server down (a no-op when it is not running).
func (m *Manager) Stop() {
	m.mu.Lock()
	srv, done := m.srv, m.done
	m.srv, m.done = nil, nil
	m.status = Status{}
	m.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
	<-done
}

func (m *Manager) setErr(err error) {
	m.mu.Lock()
	m.status = Status{Err: err.Error()}
	m.mu.Unlock()
}

// DisplayAddr turns a listener address into something to put in a browser:
// wildcard hosts become the computer's first private IPv4 address (or localhost).
func DisplayAddr(a string) string {
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return a
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = LocalIP()
	}
	return net.JoinHostPort(host, port)
}

// LocalIP returns this computer's first private IPv4 address, or "localhost".
func LocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
					return ip.String()
				}
			}
		}
	}
	return "localhost"
}

// URL returns the address to open in a browser on this computer.
func (s Status) URL() string {
	if !s.Running {
		return ""
	}
	_, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return "http://" + s.Addr
	}
	return fmt.Sprintf("http://localhost:%s", port)
}
