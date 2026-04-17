//go:build windows

package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
)

type managerService struct{}

func (m *managerService) Execute(args []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	stop := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- runManager(stop)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				close(stop)
				err := waitRunDone(runDone)
				if err != nil {
					return false, 1
				}
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
			}
		case err := <-runDone:
			if err != nil {
				changes <- svc.Status{State: svc.StopPending}
				return false, 1
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func waitRunDone(runDone <-chan error) error {
	select {
	case err := <-runDone:
		return err
	case <-time.After(20 * time.Second):
		return fmt.Errorf("timeout waiting manager shutdown")
	}
}

func tryRunWindowsService() (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false, err
	}
	if !isService {
		return false, nil
	}

	if err := svc.Run("CloudManagerService", &managerService{}); err != nil {
		return true, err
	}
	return true, nil
}
