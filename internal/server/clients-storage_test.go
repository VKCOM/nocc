package server

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if err := MakeLoggerServer("", -1); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// Every StopClient deletes a client, which bumps a 64-bit counter atomically. On a 32-bit server
// (386, ARM) that panicked with "unaligned 64-bit atomic operation" while the counter was a plain
// int64 field behind others; this only fails when run with GOARCH=386 or on 32-bit ARM.
func TestDeleteClientOn32Bit(t *testing.T) {
	storage, _ := MakeClientsStorage(t.TempDir(), time.Minute)
	client, err := storage.OnClientConnected("c1", false)
	if err != nil {
		t.Fatal(err)
	}
	storage.DeleteClient(client)
	if storage.CompletedCount() != 1 {
		t.Errorf("completed clients = %d, want 1", storage.CompletedCount())
	}
}
