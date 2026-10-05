//go:build !linux

package driver

import (
	"errors"
	"os"
)

func enterNetNamespace(*os.File) error { return errors.New("network namespaces need Linux") }
