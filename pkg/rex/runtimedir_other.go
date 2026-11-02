//go:build !unix

package rex

import (
	"context"
	"errors"
)

var errNoRuntimeDir = errors.New("SSH targets need a private runtime directory, which lazyrex supports on unix only")

// PrepareRuntimeDir is unsupported off unix.
func PrepareRuntimeDir(string) (string, error) { return "", errNoRuntimeDir }

// OSSockets is unsupported off unix.
type OSSockets struct{}

func (OSSockets) Claim(string, string) (func(), error)                    { return nil, errNoRuntimeDir }
func (OSSockets) Prepare(string, string) error                            { return errNoRuntimeDir }
func (OSSockets) WaitReady(context.Context, string, <-chan struct{}) bool { return false }
func (OSSockets) Cleanup(string)                                          {}
