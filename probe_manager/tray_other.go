//go:build !windows
// +build !windows

package main

type trayController struct{}

func newTrayController(_ *App) *trayController {
	return &trayController{}
}

func (t *trayController) Start() {}

func (t *trayController) Stop() {}
