package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCagySidebarViewUsesHerdrJSONSocketAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Herdr uses a named-pipe transport on Windows")
	}
	dir, err := os.MkdirTemp("/tmp", "cagy-api-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "herdr.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("HERDR_SOCKET_PATH", path)

	requests := make(chan map[string]any, 1)
	errors := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			errors <- err
			return
		}
		defer connection.Close()
		line, err := bufio.NewReader(connection).ReadBytes('\n')
		if err != nil {
			errors <- err
			return
		}
		var request map[string]any
		if err := json.Unmarshal(line, &request); err != nil {
			errors <- err
			return
		}
		requests <- request
		_, err = connection.Write([]byte(`{"id":"cagy:sidebar","result":{"type":"agent_view","active":true,"source":"cagy:sidebar","label":"cagy"}}` + "\n"))
		if err != nil {
			errors <- err
		}
	}()

	if err := New(&fakeRunner{}).SetCagySidebarCompact(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	case request := <-requests:
		if request["method"] != "agent.view.set" {
			t.Fatalf("request=%#v", request)
		}
		params, ok := request["params"].(map[string]any)
		if !ok || params["source"] != "cagy:sidebar" {
			t.Fatalf("params=%#v", request["params"])
		}
	}
}
