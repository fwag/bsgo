package history

import "github.com/openSUSE/bsgo/pkg/recipe/internal/gitrepo"

// Tests serve repositories from disk.
func init() { gitrepo.AllowProtocols = "file:https:http" }
