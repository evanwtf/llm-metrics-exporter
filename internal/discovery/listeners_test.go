package discovery

import (
	"context"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalListenersFindNewPort(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unsupported OS")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := LocalListeners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range got {
		if target.URL == "http://"+l.Addr().String() {
			return
		}
	}
	t.Fatal("new local listener not discovered")
}

func TestProcListenersIgnoreNonLoopbackAndNonListeners(t *testing.T) {
	input := `  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
 0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 123
 1: 00000000:1F91 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 124
 2: 0100007F:1F92 00000000:0000 01 00000000:00000000 00:00000000 00000000 1000 0 125
 3: 010200C0:1F93 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 126
`
	got, err := parseProc(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].URL != "http://127.0.0.1:8080" || got[0].Generation != "123" {
		t.Fatalf("%+v", got)
	}
}
