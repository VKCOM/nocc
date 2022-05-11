package client

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
)

// rpcStatusRes is an intermediate structure describing the rpc /Status request
type rpcStatusRes struct {
	reply          *pb.StatusReply
	err            error
	remoteHostPort string
	processingTime time.Duration
}

// rpcDumpLogsRes is an intermediate structure describing the rpc /DumpLogs request
type rpcDumpLogsRes struct {
	err            error
	remoteHostPort string
	bytesReceived  []int
	processingTime time.Duration
}

// rpcDropCachesRes is an intermediate structure describing the rpc /DropAllCaches request
type rpcDropCachesRes struct {
	reply          *pb.DropAllCachesReply
	err            error
	remoteHostPort string
	processingTime time.Duration
}

func requestRemoteStatusOne(remoteHostPort string, resChannel chan rpcStatusRes) {
	start := time.Now()
	grpcClient, err := MakeGRPCClient(remoteHostPort)
	if err != nil {
		resChannel <- rpcStatusRes{err: err, remoteHostPort: remoteHostPort}
		return
	}
	defer grpcClient.Clear()

	reply, err := grpcClient.pb.Status(grpcClient.callContext, &pb.StatusRequest{})
	resChannel <- rpcStatusRes{
		reply:          reply,
		err:            err,
		remoteHostPort: remoteHostPort,
		processingTime: time.Since(start),
	}
}

func requestRemoteDumpLogsOne(remoteHostPort string, dumpToFolder string, resChannel chan rpcDumpLogsRes) {
	start := time.Now()
	grpcClient, err := MakeGRPCClient(remoteHostPort)
	if err != nil {
		resChannel <- rpcDumpLogsRes{err: err, remoteHostPort: remoteHostPort}
		return
	}
	defer grpcClient.Clear()

	stream, err := grpcClient.pb.DumpLogs(grpcClient.callContext, &pb.DumpLogsRequest{})
	bytesReceived := make([]int, 0)

	for {
		firstChunk, err := stream.Recv()
		if err != nil {
			resChannel <- rpcDumpLogsRes{err: err, remoteHostPort: remoteHostPort}
			return
		}
		if firstChunk.LogFileExt == "" { // means end of stream, all log files sent
			break
		}

		logOutFile := path.Join(dumpToFolder, strings.Split(remoteHostPort, ":")[0]+firstChunk.LogFileExt)
		nBytes, err := receiveLogFileByChunks(stream, firstChunk, logOutFile)
		if err != nil {
			resChannel <- rpcDumpLogsRes{err: err, remoteHostPort: remoteHostPort}
			return
		}

		bytesReceived = append(bytesReceived, nBytes)
		// continue waiting for next log files pushed by the remote over the same stream
	}

	resChannel <- rpcDumpLogsRes{
		err:            err,
		remoteHostPort: remoteHostPort,
		bytesReceived:  bytesReceived,
		processingTime: time.Since(start),
	}
}

func requestDropAllCachesOne(remoteHostPort string, resChannel chan rpcDropCachesRes) {
	start := time.Now()
	grpcClient, err := MakeGRPCClient(remoteHostPort)
	if err != nil {
		resChannel <- rpcDropCachesRes{err: err, remoteHostPort: remoteHostPort}
		return
	}
	defer grpcClient.Clear()

	reply, err := grpcClient.pb.DropAllCaches(grpcClient.callContext, &pb.DropAllCachesRequest{})
	resChannel <- rpcDropCachesRes{
		reply:          reply,
		err:            err,
		remoteHostPort: remoteHostPort,
		processingTime: time.Since(start),
	}
}

// RequestRemoteStatus sends the rpc /Status request for all hosts
// and outputs brief info about each host ending up with a grouped summary.
func RequestRemoteStatus(remoteNoccHosts []string) {
	resChannel := make(chan rpcStatusRes)
	for _, remoteHostPort := range remoteNoccHosts {
		go requestRemoteStatusOne(remoteHostPort, resChannel)
	}

	nOk := 0
	nTotal := len(remoteNoccHosts)
	uniqueVersions := make(map[string]int)
	uniqueArgs := make(map[string]int)
	uniqueGcc := make(map[string]int)
	uniqueClang := make(map[string]int)

	for range remoteNoccHosts {
		res := <-resChannel
		var reply *pb.StatusReply = res.reply

		if res.err != nil {
			fmt.Printf("Server \033[36m%s\033[0m unavailable: %v\n", res.remoteHostPort, res.err)
			continue
		}

		fmt.Printf("Server \033[36m%s\033[0m \u001B[32mok\u001B[0m (uptime %s)\n", res.remoteHostPort, time.Duration(reply.ServerUptime).Truncate(time.Second))
		fmt.Println("  Processing time:", res.processingTime.Truncate(time.Microsecond))
		fmt.Println("  Log file size KB:", reply.LogFileSize/1024)
		fmt.Println("  Src cache size KB:", reply.SrcCacheSize/1024)
		fmt.Println("  Obj cache size KB:", reply.ObjCacheSize/1024)
		fmt.Println("  nocc-server version:", reply.ServerVersion)
		fmt.Println("  nocc-server cmd args:", reply.ServerArgs)
		fmt.Println("  g++:", reply.GccVersion)
		fmt.Println("  clang:", reply.ClangVersion)
		if reply.ServerVersion != common.GetVersion() {
			fmt.Println("\033[36mnocc-server version differs from current client\033[0m")
		}

		nOk++
		uniqueVersions[reply.ServerVersion]++
		uniqueArgs[strings.Join(reply.ServerArgs, " ")]++
		uniqueGcc[reply.GccVersion]++
		uniqueClang[reply.ClangVersion]++
	}

	if len(remoteNoccHosts) == 1 {
		return
	}

	fmt.Printf("\033[1mSummary:\033[00m\n")
	if nOk == nTotal {
		fmt.Printf("\033[32m  ok %d / %d\033[0m\n", nOk, nTotal)
	} else {
		fmt.Printf("\033[31m  ok %d / %d\033[0m\n", nOk, nTotal)
	}
	if len(uniqueVersions) == 1 {
		fmt.Println("  all nocc versions are the same")
	} else {
		fmt.Println("\033[31m  different nocc versions\033[0m\n  ", uniqueVersions)
	}
	if len(uniqueArgs) == 1 {
		fmt.Println("  all nocc cmd args are the same")
	} else {
		fmt.Println("\033[31m  different nocc cmd args\033[0m\n  ", uniqueArgs)
	}
	if len(uniqueGcc) == 1 {
		fmt.Println("  all g++ versions are the same")
	} else {
		fmt.Println("\033[31m  different g++ versions\033[0m\n  ", uniqueGcc)
	}
	if len(uniqueClang) == 1 {
		fmt.Println("  all clang versions are the same")
	} else {
		fmt.Println("\033[31m  different clang versions\033[0m\n  ", uniqueClang)
	}
}

// RequestRemoteDumpLogs sends the rpc /DumpLogs request for all hosts
// and saves all logs to dumpToFolder (inside /tmp in reality).
func RequestRemoteDumpLogs(remoteNoccHosts []string, dumpToFolder string) {
	_ = os.RemoveAll(dumpToFolder)
	if err := os.MkdirAll(dumpToFolder, os.ModePerm); err != nil {
		logClient.Error(err)
	}

	resChannel := make(chan rpcDumpLogsRes)
	for _, remoteHostPort := range remoteNoccHosts {
		go requestRemoteDumpLogsOne(remoteHostPort, dumpToFolder, resChannel)
	}

	nOk := 0
	nTotal := len(remoteNoccHosts)

	for range remoteNoccHosts {
		res := <-resChannel

		if res.err != nil {
			fmt.Printf("Server \033[36m%s\033[0m unavailable: %v\n", res.remoteHostPort, res.err)
			continue
		}

		strBytes := ""
		for _, nBytes := range res.bytesReceived {
			strBytes += fmt.Sprintf("%d ", nBytes)
		}
		fmt.Printf("Server \033[36m%s\033[0m dumped %d files (%sbytes)\n", res.remoteHostPort, len(res.bytesReceived), strBytes)
		nOk++
	}

	if nOk == nTotal {
		fmt.Printf("\033[32mdumped %d / %d\033[0m to folder %s\n", nOk, nTotal, dumpToFolder)
	} else {
		fmt.Printf("\033[31mdumped %d / %d\033[0m to folder %s\n", nOk, nTotal, dumpToFolder)
	}
}

// RequestDropAllCaches sends the rpc /DropAllCaches request for all hosts.
// Used primarily for development purposes.
func RequestDropAllCaches(remoteNoccHosts []string) {
	resChannel := make(chan rpcDropCachesRes)
	for _, remoteHostPort := range remoteNoccHosts {
		go requestDropAllCachesOne(remoteHostPort, resChannel)
	}

	nOk := 0
	nTotal := len(remoteNoccHosts)

	for range remoteNoccHosts {
		res := <-resChannel
		var reply *pb.DropAllCachesReply = res.reply

		if res.err != nil {
			fmt.Printf("Server \033[36m%s\033[0m unavailable: %v\n", res.remoteHostPort, res.err)
			continue
		}

		fmt.Printf("Server \033[36m%s\033[0m dropped %d src files and %d obj files\n", res.remoteHostPort, reply.DroppedSrcFiles, reply.DroppedObjFiles)
		nOk++
	}

	if nOk == nTotal {
		fmt.Printf("\033[32mdropped %d / %d\033[0m\n", nOk, nTotal)
	} else {
		fmt.Printf("\033[31mdropped %d / %d\033[0m\n", nOk, nTotal)
	}
}
