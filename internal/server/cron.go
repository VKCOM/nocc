package server

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Cron calls doCron(), which ticks in the background and used to write stats, delete inactive clients, etc.
type Cron struct {
	stopFlag bool
	signals  chan os.Signal

	noccServer *NoccServer
}

func MakeCron(noccServer *NoccServer) (*Cron, error) {
	return &Cron{
		noccServer: noccServer,
	}, nil
}

func (c *Cron) doCron() {
	const cronTickInterval = 5 * time.Second

	for !c.stopFlag {
		cronStartTime := time.Now()

		c.noccServer.Stats.SendToStatsd(c.noccServer)
		c.noccServer.SrcFileCache.PurgeLastElementsIfRequired()
		c.noccServer.ObjFileCache.PurgeLastElementsIfRequired()
		c.noccServer.ActiveClients.DeleteInactiveClients()

		sleepTime := cronTickInterval - time.Since(cronStartTime)
		if sleepTime <= 0 {
			sleepTime = time.Nanosecond
		}
		for sleepTime > 0 {
			select {
			case sig := <-c.signals:
				logServer.Info(0, "got signal", sig)
				if false { //sig == syscall.SIGUSR1 {
					if err := logServer.RotateLogFile(); err != nil {
						logServer.Error("could not rotate log file", err)
					} else {
						logServer.Info(0, "log file rotated")
					}
				} else if sig == syscall.SIGTERM {
					go c.noccServer.QuitServerGracefully()
				}
			case <-time.After(sleepTime):
				break
			}
			sleepTime = cronTickInterval - time.Since(cronStartTime)
		}
	}
}

func (c *Cron) StartCron() {
	c.signals = make(chan os.Signal, 2)
	// signal.Notify(c.signals, syscall.SIGUSR1, syscall.SIGTERM)
	signal.Notify(c.signals, syscall.SIGTERM)
	c.doCron()
}

func (c *Cron) StopCron() {
	c.stopFlag = true
	// don't wait here; doCron() is now sleeping, it won't prevent process from exiting
}
