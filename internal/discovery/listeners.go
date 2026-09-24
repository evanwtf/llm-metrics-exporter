// Package discovery identifies currently observable local inference servers.
// It never launches models, issues inference requests or scans a network.
package discovery

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Target is a loopback HTTP endpoint. Generation is optional OS evidence of
// replacement (socket inode on Linux, PID on macOS), never a series label.
type Target struct{ URL, Generation string }

// LocalTarget validates and canonicalizes explicit discovery endpoints. No
// DNS, proxy, URL credentials, queries, paths or redirects can expand scope.
func LocalTarget(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, errors.New("invalid discovery endpoint")
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return Target{}, errors.New("discovery endpoints must be plain loopback HTTP base URLs")
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return Target{}, errors.New("discovery endpoint must be a loopback IP or localhost")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return Target{}, errors.New("invalid discovery port")
	}
	return Target{URL: "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(p))}, nil
}

// LocalListeners enumerates loopback-reachable TCP listeners without needing
// root or a Docker socket. Reads/command output are capped at 1 MiB each.
func LocalListeners(ctx context.Context) ([]Target, error) {
	var out []Target
	switch runtime.GOOS {
	case "linux":
		for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f, err := os.Open(path)
			if os.IsNotExist(err) && strings.HasSuffix(path, "6") {
				continue
			}
			if err != nil {
				return nil, err
			}
			body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
			f.Close()
			if len(body) > 1<<20 {
				return nil, errors.New("listener table too large")
			}
			if err != nil {
				return nil, err
			}
			targets, err := parseProc(bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			out = append(out, targets...)
		}
	case "darwin":
		cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpn")
		var b limitedBuffer
		cmd.Stdout = &b
		err := cmd.Run()
		if err != nil {
			return nil, errors.New("cannot enumerate listeners with lsof")
		}
		pid := ""
		for _, line := range strings.Split(b.String(), "\n") {
			if strings.HasPrefix(line, "p") {
				pid = line[1:]
			}
			if !strings.HasPrefix(line, "n") {
				continue
			}
			addr := strings.Replace(line[1:], "*:", "127.0.0.1:", 1)
			if target, err := LocalTarget("http://" + addr); err == nil {
				target.Generation = pid
				out = append(out, target)
			}
		}
	default:
		return nil, errors.New("local discovery unsupported on this OS; use explicit loopback discovery endpoints")
	}
	// One loopback service per port: prefer IPv4 when both families listen.
	// This avoids double-counting a typical dual-stack listener.
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	seen := map[string]bool{}
	unique := out[:0]
	for _, t := range out {
		u, _ := url.Parse(t.URL)
		if !seen[u.Port()] {
			seen[u.Port()] = true
			unique = append(unique, t)
		}
	}
	return unique, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("listener output too large")
	}
	return b.Buffer.Write(p)
}

func parseProc(r io.Reader) ([]Target, error) {
	s := bufio.NewScanner(r)
	var out []Target
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 10 || f[3] != "0A" {
			continue
		}
		addr, port, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		host := ""
		switch addr {
		case "00000000", "0100007F":
			host = "127.0.0.1"
		case "00000000000000000000000000000000", "00000000000000000000000001000000":
			host = "::1"
		default:
			continue
		}
		p, err := strconv.ParseUint(port, 16, 16)
		if err != nil || p == 0 {
			continue
		}
		out = append(out, Target{URL: "http://" + net.JoinHostPort(host, strconv.Itoa(int(p))), Generation: f[9]})
	}
	return out, s.Err()
}
