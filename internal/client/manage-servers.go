package client

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

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
	if err != nil {
		resChannel <- rpcDumpLogsRes{err: err, remoteHostPort: remoteHostPort}
		return
	}
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

		logOutFile := path.Join(dumpToFolder, ExtractRemoteHostWithoutPort(remoteHostPort)+firstChunk.LogFileExt)
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
	noccVersionsByRemote := make(map[string][]string)
	noccServerArgsByRemote := make(map[string][]string)
	gccVersionsByRemote := make(map[string][]string)
	clangVersionsByRemote := make(map[string][]string)
	ulimitByRemote := make(map[string][]string)
	unameByRemote := make(map[string][]string)

	addByRemote := func(mapByRemote map[string][]string, key string, remoteHost string) {
		if _, ok := mapByRemote[key]; !ok {
			mapByRemote[key] = make([]string, 0)
		}
		mapByRemote[key] = append(mapByRemote[key], remoteHost)
	}

	for range remoteNoccHosts {
		res := <-resChannel
		var r *pb.StatusReply = res.reply
		remoteHost := ExtractRemoteHostWithoutPort(res.remoteHostPort)

		if res.err != nil {
			fmt.Printf("Server \033[36m%s\033[0m \033[31munavailable\033[0m: %v\n", remoteHost, res.err)
			continue
		}

		fmt.Printf("Server \033[36m%s\033[0m \033[32mok\033[0m (uptime %s)\n", remoteHost, time.Duration(r.ServerUptime).Truncate(time.Second))
		fmt.Printf("  Processing time: %d ms\n", res.processingTime.Milliseconds())
		fmt.Printf("  Disk consumption: log %d KB, src cache %d KB, obj cache %d KB\n", r.LogFileSize/1024, r.SrcCacheSize/1024, r.ObjCacheSize/1024)
		fmt.Printf("  Activity: sessions total %d, active %d\n", r.SessionsTotal, r.SessionsActive)
		fmt.Printf("  Cxx: calls %d, more10sec %d, more30sec %d\n", r.CxxCalls, r.CxxDurMore10Sec, r.CxxDurMore30Sec)

		if len(r.UniqueRemotes) > 1 {
			fmt.Printf("  \033[31mnon-unique remotes\033[0m:\n")
			for _, u := range r.UniqueRemotes {
				fmt.Printf("    %s\n", u)
			}
		}

		nOk++
		addByRemote(noccVersionsByRemote, r.ServerVersion, remoteHost)
		addByRemote(noccServerArgsByRemote, strings.Join(r.ServerArgs, " "), remoteHost)
		addByRemote(gccVersionsByRemote, r.GccVersion, remoteHost)
		addByRemote(clangVersionsByRemote, r.ClangVersion, remoteHost)
		addByRemote(ulimitByRemote, fmt.Sprintf("ulimit %d", r.ULimit), remoteHost)
		addByRemote(unameByRemote, r.UName, remoteHost)
	}

	if len(remoteNoccHosts) == 1 || nOk == 0 {
		return
	}

	printEqualOfDiff := func(mapByRemote map[string][]string, msgAllEqual string, msgDiff string) {
		if len(mapByRemote) == 1 {
			var firstKey string
			for k := range mapByRemote {
				firstKey = k
				break
			}
			fmt.Printf("  %s:\n    %s\n", msgAllEqual, firstKey)
			return
		}
		fmt.Printf("\033[31m  %s\033[0m\n", msgDiff)
		for k, hosts := range mapByRemote {
			fmt.Printf("    * \033[1m%s\033[0m\n      %s\n", k, strings.Join(hosts, ", "))
		}
	}

	fmt.Printf("\033[1mSummary:\033[00m\n")
	if nOk == nTotal {
		fmt.Printf("\033[32m  ok %d / %d\033[0m\n", nOk, nTotal)
	} else {
		fmt.Printf("\033[31m  ok %d / %d\033[0m\n", nOk, nTotal)
	}
	printEqualOfDiff(noccVersionsByRemote, "nocc versions equal", "different nocc versions")
	printEqualOfDiff(noccServerArgsByRemote, "nocc cmd args equal", "different nocc cmd args")
	printEqualOfDiff(gccVersionsByRemote, "g++ versions equal", "different g++ versions")
	printEqualOfDiff(clangVersionsByRemote, "clang versions equal", "different clang versions")
	printEqualOfDiff(ulimitByRemote, "ulimit equal", "different ulimit")
	printEqualOfDiff(unameByRemote, "uname equal", "different uname")
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
		remoteHost := ExtractRemoteHostWithoutPort(res.remoteHostPort)

		if res.err != nil {
			fmt.Printf("Server \033[36m%s\033[0m unavailable: %v\n", remoteHost, res.err)
			continue
		}

		strBytes := ""
		for _, nBytes := range res.bytesReceived {
			strBytes += fmt.Sprintf("%d ", nBytes)
		}
		fmt.Printf("Server \033[36m%s\033[0m dumped %d files (%sbytes)\n", remoteHost, len(res.bytesReceived), strBytes)
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
		remoteHost := ExtractRemoteHostWithoutPort(res.remoteHostPort)

		if res.err != nil {
			fmt.Printf("Server \033[36m%s\033[0m unavailable: %v\n", remoteHost, res.err)
			continue
		}

		fmt.Printf("Server \033[36m%s\033[0m dropped %d src files and %d obj files\n", remoteHost, reply.DroppedSrcFiles, reply.DroppedObjFiles)
		nOk++
	}

	if nOk == nTotal {
		fmt.Printf("\033[32mdropped %d / %d\033[0m\n", nOk, nTotal)
	} else {
		fmt.Printf("\033[31mdropped %d / %d\033[0m\n", nOk, nTotal)
	}
}
