package server

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ClientsStorage contains all active clients connected to this server.
// After a client is not active for some time, it's deleted (and its working directory is removed from a hard disk).
type ClientsStorage struct {
	table map[string]*Client
	mu    sync.RWMutex

	clientsDir string // /tmp/nocc/cpp/clients

	completedCount       int64
	lastPurgeTime        time.Time
	checkInactiveTimeout time.Duration

	uniqueRemotesList map[string]string
}

func MakeClientsStorage(clientsDir string, checkInactiveTimeout time.Duration) (*ClientsStorage, error) {
	return &ClientsStorage{
		table:                make(map[string]*Client, 1024),
		clientsDir:           clientsDir,
		uniqueRemotesList:    make(map[string]string, 1),
		checkInactiveTimeout: checkInactiveTimeout,
	}, nil
}

func (allClients *ClientsStorage) GetClient(clientID string) *Client {
	allClients.mu.RLock()
	client := allClients.table[clientID]
	allClients.mu.RUnlock()

	return client
}

func (allClients *ClientsStorage) OnClientConnected(clientID string, disableObjCache bool) (*Client, error) {
	allClients.mu.RLock()
	client := allClients.table[clientID]
	allClients.mu.RUnlock()

	// rpc query /StartClient is sent exactly once by nocc-daemon
	// if this clientID exists in table, this means a previous interrupted nocc-daemon launch
	// in this case, delete an old hanging client, closing all channels and streams — and create a new instance
	if client != nil {
		logServer.Info(0, "client reconnected, re-creating", "clientID", clientID)
		allClients.DeleteClient(client)
	}

	workingDir := path.Join(allClients.clientsDir, clientID)
	if err := os.Mkdir(workingDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("can't create client working directory: %v", err)
	}

	client = &Client{
		clientID:          clientID,
		workingDir:        workingDir,
		lastSeen:          time.Now(),
		sessions:          make(map[uint32]*Session, 20),
		files:             make(map[string]*fileInClientDir, 1024),
		dirs:              make(map[string]bool, 100),
		chanDisconnected:  make(chan struct{}),
		chanReadySessions: make(chan *Session, 200),
		disableObjCache:   disableObjCache,
	}

	allClients.mu.Lock()
	allClients.table[clientID] = client
	allClients.mu.Unlock()
	return client, nil
}

func (allClients *ClientsStorage) DeleteClient(client *Client) {
	allClients.mu.Lock()
	delete(allClients.table, client.clientID)
	allClients.mu.Unlock()
	atomic.AddInt64(&allClients.completedCount, 1)

	close(client.chanDisconnected)
	// don't close chanReadySessions intentionally, it's not a leak
	client.RemoveWorkingDir()
}

func (allClients *ClientsStorage) DeleteInactiveClients() {
	now := time.Now()
	if now.Sub(allClients.lastPurgeTime) < time.Minute {
		return
	}

	for {
		var inactiveClient *Client = nil
		allClients.mu.RLock()
		for _, client := range allClients.table {
			if now.Sub(client.lastSeen) > allClients.checkInactiveTimeout {
				inactiveClient = client
				break
			}
		}
		allClients.mu.RUnlock()
		if inactiveClient == nil {
			break
		}

		logServer.Info(0, "delete inactive client", "clientID", inactiveClient.clientID, "num files", inactiveClient.FilesCount(), "; nClients", allClients.ActiveCount()-1)
		allClients.DeleteClient(inactiveClient)
	}
}

func (allClients *ClientsStorage) StopAllClients() {
	allClients.mu.Lock()
	for _, client := range allClients.table {
		// do not call DeleteClient(), since the server is stopping, removing working dir is not needed
		close(client.chanDisconnected)
	}

	allClients.table = make(map[string]*Client)
	allClients.mu.Unlock()
}

func (allClients *ClientsStorage) ActiveCount() int64 {
	allClients.mu.RLock()
	clientsCount := len(allClients.table)
	allClients.mu.RUnlock()
	return int64(clientsCount)
}

func (allClients *ClientsStorage) CompletedCount() int64 {
	return atomic.LoadInt64(&allClients.completedCount)
}

func (allClients *ClientsStorage) ActiveSessionsCount() int64 {
	allClients.mu.RLock()
	sessionsCount := 0
	for _, client := range allClients.table {
		sessionsCount += client.GetActiveSessionsCount()
	}
	allClients.mu.RUnlock()
	return int64(sessionsCount)
}

func (allClients *ClientsStorage) TotalFilesCountInDirs() int64 {
	var filesCount int64 = 0
	allClients.mu.RLock()
	for _, client := range allClients.table {
		filesCount += client.FilesCount()
	}
	allClients.mu.RUnlock()
	return filesCount
}

// IsRemotesListSeenTheFirstTime maintains allClients.uniqueRemotesList.
// It's mostly for debug purposes — to detect clients with strange NOCC_SERVERS env.
// Probably, will be deleted in the future.
func (allClients *ClientsStorage) IsRemotesListSeenTheFirstTime(allRemotesDelim string, clientID string) bool {
	allClients.mu.RLock()
	_, exists := allClients.uniqueRemotesList[allRemotesDelim]
	allClients.mu.RUnlock()

	if !exists {
		allClients.mu.Lock()
		allClients.uniqueRemotesList[allRemotesDelim] = clientID
		allClients.mu.Unlock()
	}

	return !exists
}

func (allClients *ClientsStorage) GetUniqueRemotesListInfo() (uniqueInfo []string) {
	allClients.mu.RLock()

	uniqueInfo = make([]string, 0, len(allClients.uniqueRemotesList))
	for allRemotesDelim, clientID := range allClients.uniqueRemotesList {
		nRemotes := strings.Count(allRemotesDelim, ",") + 1
		uniqueInfo = append(uniqueInfo, fmt.Sprintf("(n=%d) clientID %s : %s", nRemotes, clientID, allRemotesDelim))
	}

	allClients.mu.RUnlock()
	return
}
